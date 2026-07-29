package pulseapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/try-pulse/pulse-import/internal/version"
)

const (
	maxAttempts      = 4
	maxErrorBodySize = 64 << 10
	maxRetryDelay    = 30 * time.Second
)

type Client struct {
	BaseURL     string
	Token       string
	WorkspaceID string
	HTTP        *http.Client
	Backoff     func(attempt int) time.Duration
	UserAgent   string
}

func New(baseURL, token, workspaceID string) (*Client, error) {
	normalized, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:     normalized,
		Token:       token,
		WorkspaceID: workspaceID,
		HTTP:        &http.Client{Timeout: 60 * time.Second},
		Backoff: func(attempt int) time.Duration {
			delay := time.Second << attempt
			if delay > maxRetryDelay {
				return maxRetryDelay
			}
			return delay
		},
		UserAgent: "pulse-import/" + version.Current(),
	}, nil
}

func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid Pulse API URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid Pulse API URL: scheme must be http or https")
	}
	if u.Host == "" || u.User != nil {
		return "", fmt.Errorf("invalid Pulse API URL: host is required and credentials are not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid Pulse API URL: query and fragment are not allowed")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func (c *Client) InsecureRemote() bool {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && host != "127.0.0.1" && host != "::1"
}

type APIError struct {
	Status     int
	Code       string
	Message    string
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("pulse api %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("pulse api %d: %s", e.Status, e.Body)
}

// IsAmbiguousWriteError reports errors for which a POST may have reached Pulse even
// though the client did not receive a definitive success response.
func IsAmbiguousWriteError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true
}

func (c *Client) doRaw(ctx context.Context, method, path string, body any) ([]byte, error) {
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		data, err := c.roundTrip(ctx, method, path, body, "", nil)
		if err == nil {
			return data, nil
		}
		last = err
		apiErr, ok := err.(*APIError)
		retry := ok && apiErr.Status == http.StatusTooManyRequests
		if method == http.MethodGet {
			retry = retry || !ok || apiErr.Status >= http.StatusInternalServerError
		}
		if !retry || attempt == maxAttempts-1 || ctx.Err() != nil {
			return nil, err
		}
		var retryAfter time.Duration
		if ok {
			retryAfter = apiErr.RetryAfter
		}
		if err := c.wait(ctx, attempt, retryAfter); err != nil {
			return nil, err
		}
	}
	return nil, last
}

func (c *Client) wait(ctx context.Context, attempt int, retryAfter time.Duration) error {
	d := retryAfter
	if d <= 0 {
		d = time.Second
	}
	if c.Backoff != nil {
		if fallback := c.Backoff(attempt); retryAfter <= 0 {
			d = fallback
		}
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) roundTrip(ctx context.Context, method, path string, jsonBody any, contentType string, rawBody io.Reader) ([]byte, error) {
	rdr := rawBody
	if jsonBody != nil {
		b, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", c.WorkspaceID)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var data []byte
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, err = readBounded(resp.Body, maxErrorBodySize)
	} else {
		data, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(data, &errBody)
		msg := errBody.Message
		if msg == "" {
			msg = errBody.Error
		}
		return nil, &APIError{
			Status:     resp.StatusCode,
			Code:       errBody.Code,
			Message:    msg,
			Body:       string(data),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	return data, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) <= limit {
		return data, nil
	}
	return append(data[:limit], []byte("\n… response truncated")...), nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func decode[T any](data []byte) (T, error) {
	var out T
	if len(data) == 0 || string(data) == "null" {
		return out, fmt.Errorf("empty response")
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

func get[T any](c *Client, ctx context.Context, path string) (T, error) {
	data, err := c.doRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		var zero T
		return zero, err
	}
	return decode[T](data)
}

func post[T any](c *Client, ctx context.Context, path string, body any) (T, error) {
	data, err := c.doRaw(ctx, http.MethodPost, path, body)
	if err != nil {
		var zero T
		return zero, err
	}
	return decode[T](data)
}

type page[T any] struct {
	Data []T `json:"data"`
}

func listAll[T any](c *Client, ctx context.Context, path string) ([]T, error) {
	var all []T
	pageNum := 1
	for {
		q := url.Values{}
		q.Set("page", fmt.Sprintf("%d", pageNum))
		q.Set("limit", "100")
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		out, err := get[page[T]](c, ctx, path+sep+q.Encode())
		if err != nil {
			return nil, err
		}
		all = append(all, out.Data...)
		if len(out.Data) < 100 {
			break
		}
		pageNum++
	}
	return all, nil
}

type User struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	DisplayName string `json:"display_name"`
}

type Workspace struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type WorkspaceMembership struct {
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	Workspace *Workspace `json:"workspace"`
}

type Team struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IconCode  string `json:"icon_code"`
	IconColor string `json:"icon_color"`
}

type Project struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	TeamID string `json:"team_id"`
}

type Label struct {
	ID         string `json:"id"`
	TeamID     string `json:"team_id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	EntityType string `json:"entity_type"`
	Archived   bool   `json:"archived"`
}

type Issue struct {
	ID          string  `json:"id"`
	Code        string  `json:"code"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	Type        string  `json:"type"`
	TeamID      string  `json:"team_id"`
	ProjectID   *string `json:"project_id,omitempty"`
	AssigneeID  *string `json:"assignee_id,omitempty"`
	MainDocID   *string `json:"main_doc_id,omitempty"`
}

type CreateIssueRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	Type        string   `json:"type,omitempty"`
	TeamID      string   `json:"team_id"`
	ProjectID   *string  `json:"project_id,omitempty"`
	AssigneeID  *string  `json:"assignee_id,omitempty"`
	LabelIDs    []string `json:"label_ids,omitempty"`
}

type CreateLabelRequest struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
	EntityType  string `json:"entity_type"`
}

type AttachmentRequest struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	IsMainDoc  bool   `json:"is_main_doc,omitempty"`
}

type Document struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (c *Client) Me(ctx context.Context) (*User, error) {
	wrap, err := get[struct {
		User User `json:"user"`
	}](c, ctx, "/auth/me")
	if err != nil {
		return nil, err
	}
	if wrap.User.ID == "" {
		return nil, fmt.Errorf("auth/me: empty user")
	}
	return &wrap.User, nil
}

func (c *Client) ListMyWorkspaces(ctx context.Context) ([]WorkspaceMembership, error) {
	out, err := get[page[WorkspaceMembership]](c, ctx, "/workspaces/me")
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *Client) ListTeams(ctx context.Context) ([]Team, error) {
	return listAll[Team](c, ctx, "/teams")
}

func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	return listAll[User](c, ctx, "/users")
}

func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	return listAll[Project](c, ctx, "/projects")
}

func (c *Client) ListLabels(ctx context.Context, teamID string) ([]Label, error) {
	path := fmt.Sprintf("/teams/%s/labels?archived=false&entity_type=issue", url.PathEscape(teamID))
	out, err := get[page[Label]](c, ctx, path)
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *Client) CreateLabel(ctx context.Context, teamID string, req CreateLabelRequest) (*Label, error) {
	label, err := post[Label](c, ctx, fmt.Sprintf("/teams/%s/labels", url.PathEscape(teamID)), req)
	if err != nil {
		return nil, err
	}
	if label.ID == "" {
		return nil, fmt.Errorf("create label: empty response")
	}
	return &label, nil
}

func (c *Client) CreateIssue(ctx context.Context, req CreateIssueRequest) (*Issue, error) {
	issue, err := post[Issue](c, ctx, "/issues", req)
	if err != nil {
		return nil, err
	}
	if issue.ID == "" {
		return nil, fmt.Errorf("create issue: empty response")
	}
	return &issue, nil
}

func (c *Client) GetIssue(ctx context.Context, issueID string) (*Issue, error) {
	issue, err := get[Issue](c, ctx, "/issues/"+url.PathEscape(issueID))
	if err != nil {
		return nil, err
	}
	if issue.ID == "" {
		return nil, fmt.Errorf("get issue: empty response")
	}
	return &issue, nil
}

func (c *Client) UploadMainDoc(ctx context.Context, issueID, title string, plateJSON []byte) (*Document, error) {
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		doc, err := c.uploadMainDocOnce(ctx, issueID, title, plateJSON)
		if err == nil {
			return doc, nil
		}
		last = err
		apiErr, ok := err.(*APIError)
		if !ok || apiErr.Status != 429 || attempt == maxAttempts-1 {
			return nil, err
		}
		if err := c.wait(ctx, attempt, apiErr.RetryAfter); err != nil {
			return nil, err
		}
	}
	return nil, last
}

func (c *Client) uploadMainDocOnce(ctx context.Context, issueID, title string, plateJSON []byte) (*Document, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", sanitizeFileName(title))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(plateJSON); err != nil {
		return nil, err
	}
	_ = w.WriteField("title", truncateRunes(title, 200))
	atts, _ := json.Marshal([]AttachmentRequest{{
		EntityType: "issue", EntityID: issueID, IsMainDoc: true,
	}})
	_ = w.WriteField("attachments", string(atts))
	if err := w.Close(); err != nil {
		return nil, err
	}

	data, err := c.roundTrip(ctx, http.MethodPost, "/content/documents/upload", nil, w.FormDataContentType(), &buf)
	if err != nil {
		return nil, err
	}

	if doc, err := decode[Document](data); err == nil && doc.ID != "" {
		return &doc, nil
	}
	if wrap, err := decode[struct {
		Content Document `json:"content"`
	}](data); err == nil && wrap.Content.ID != "" {
		return &wrap.Content, nil
	}
	return nil, fmt.Errorf("upload main doc: response missing document id")
}

func sanitizeFileName(title string) string {
	b := strings.Builder{}
	for _, r := range title {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	s := b.String()
	if s == "" {
		s = "document"
	}
	return truncateRunes(s, 80) + ".json"
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

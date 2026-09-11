// Package github is a minimal client for the GitHub REST API endpoints the
// reviewer needs. It deliberately covers only those endpoints instead of
// pulling in a full SDK.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/OtavioMendes12/Sentinel-AI/internal/domain"
	"github.com/OtavioMendes12/Sentinel-AI/internal/httpx"
)

const (
	apiVersion    = "2022-11-28"
	userAgent     = "sentinel-ai-reviewer"
	filesPerPage  = 100
	maxFilesPages = 30        // GitHub lists at most 3000 files per pull request
	maxBodyBytes  = 10 << 20  // per response
	maxPatchBytes = 256 << 10 // per file; larger patches are dropped, not truncated
	maxFilesTotal = 3000
)

// Client talks to the GitHub REST API. The token is only ever placed in the
// Authorization header.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	retry   httpx.RetryPolicy
}

// NewClient returns a client for baseURL (https://api.github.com, or the
// GitHub Enterprise Server API URL).
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpClient,
		retry:   httpx.DefaultRetryPolicy,
	}
}

type prResponse struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Base   struct {
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// PullRequest fetches a pull request's metadata.
func (c *Client) PullRequest(ctx context.Context, repo string, number int) (*domain.PullRequest, error) {
	var pr prResponse
	if err := c.get(ctx, repoPath(repo, "pulls", fmt.Sprint(number)), nil, &pr); err != nil {
		return nil, fmt.Errorf("fetching pull request %s#%d: %w", repo, number, err)
	}
	return &domain.PullRequest{
		Repository: repo,
		Number:     pr.Number,
		Title:      pr.Title,
		Body:       pr.Body,
		State:      pr.State,
		Draft:      pr.Draft,
		BaseSHA:    pr.Base.SHA,
		HeadSHA:    pr.Head.SHA,
	}, nil
}

type fileResponse struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
}

// PullRequestFiles lists every file changed by a pull request, following
// pagination.
func (c *Client) PullRequestFiles(ctx context.Context, repo string, number int) ([]domain.ChangedFile, error) {
	var files []domain.ChangedFile
	for page := 1; page <= maxFilesPages; page++ {
		query := url.Values{"per_page": {fmt.Sprint(filesPerPage)}, "page": {fmt.Sprint(page)}}
		var batch []fileResponse
		if err := c.get(ctx, repoPath(repo, "pulls", fmt.Sprint(number), "files"), query, &batch); err != nil {
			return nil, fmt.Errorf("listing files of %s#%d: %w", repo, number, err)
		}
		for _, f := range batch {
			patch := f.Patch
			if len(patch) > maxPatchBytes {
				patch = ""
			}
			files = append(files, domain.ChangedFile{
				Path:         f.Filename,
				PreviousPath: f.PreviousFilename,
				Status:       f.Status,
				Additions:    f.Additions,
				Deletions:    f.Deletions,
				Patch:        patch,
			})
		}
		if len(batch) < filesPerPage || len(files) >= maxFilesTotal {
			break
		}
	}
	return files, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	resp, err := httpx.Do(ctx, c.http, c.retry, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		return req, nil
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return decode(resp.Body, out)
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+c.token)
}

func decode(r io.Reader, out any) error {
	body, err := httpx.ReadLimited(r, maxBodyBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// repoPath builds /repos/{owner}/{name}/..., escaping every segment.
func repoPath(repo string, segments ...string) string {
	owner, name, _ := strings.Cut(repo, "/")
	parts := append([]string{"repos", owner, name}, segments...)
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(parts, "/")
}

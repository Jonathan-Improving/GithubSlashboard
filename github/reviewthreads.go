package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Jonathan-Improving/githubslashboard/model"
)

// graphQLEndpoint is GitHub's GraphQL v4 endpoint. Review-thread resolution
// state (isResolved) is not exposed by the REST v3 API, so it is the only way
// to distinguish "approved and done" from "approved with comments still open".
const graphQLEndpoint = "https://api.github.com/graphql"

// reviewThreadsQuery fetches the review decision and review threads for one PR.
// It is a read-only query; the client issues no mutations (POLICY).
const reviewThreadsQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){
  repository(owner:$owner,name:$name){
    pullRequest(number:$number){
      reviewDecision
      reviewThreads(first:100,after:$cursor){
        pageInfo{ hasNextPage endCursor }
        nodes{
          isResolved
          isOutdated
          comments(first:1){ nodes{ author{ login } createdAt } }
        }
      }
    }
  }
}`

// reviewThread is one review thread reduced to the facts we judge on.
type reviewThread struct {
	Resolved bool
	Outdated bool
	Author   string
	Created  time.Time
}

// reviewThreadsResult is the outcome of a review-thread query for one PR.
type reviewThreadsResult struct {
	Decision string
	Threads  []reviewThread
}

// graphQLResponse mirrors the reviewThreadsQuery response shape.
type graphQLResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewDecision string `json:"reviewDecision"`
				ReviewThreads  struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						IsResolved bool `json:"isResolved"`
						IsOutdated bool `json:"isOutdated"`
						Comments   struct {
							Nodes []struct {
								Author struct {
									Login string `json:"login"`
								} `json:"author"`
								CreatedAt time.Time `json:"createdAt"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// collectReviewThreads fetches the review decision and every review thread for
// a PR, paginating until exhausted.
func (c *Client) collectReviewThreads(ctx context.Context, owner, name string, number int) (reviewThreadsResult, error) {
	var out reviewThreadsResult
	cursor := ""
	for {
		vars := map[string]any{"owner": owner, "name": name, "number": number}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		body, err := json.Marshal(map[string]any{"query": reviewThreadsQuery, "variables": vars})
		if err != nil {
			return out, fmt.Errorf("marshal review-threads query: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphQLEndpoint, bytes.NewReader(body))
		if err != nil {
			return out, fmt.Errorf("build review-threads request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return out, fmt.Errorf("review-threads query: %w", err)
		}
		var parsed graphQLResponse
		decErr := json.NewDecoder(resp.Body).Decode(&parsed)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return out, fmt.Errorf("review-threads query: unexpected status %s", resp.Status)
		}
		if decErr != nil {
			return out, fmt.Errorf("decode review-threads response: %w", decErr)
		}
		if len(parsed.Errors) > 0 {
			return out, fmt.Errorf("review-threads query: %s", parsed.Errors[0].Message)
		}

		pr := parsed.Data.Repository.PullRequest
		out.Decision = pr.ReviewDecision
		for _, n := range pr.ReviewThreads.Nodes {
			t := reviewThread{Resolved: n.IsResolved, Outdated: n.IsOutdated}
			if len(n.Comments.Nodes) > 0 {
				t.Author = n.Comments.Nodes[0].Author.Login
				t.Created = n.Comments.Nodes[0].CreatedAt
			}
			out.Threads = append(out.Threads, t)
		}
		if !pr.ReviewThreads.PageInfo.HasNextPage {
			return out, nil
		}
		cursor = pr.ReviewThreads.PageInfo.EndCursor
	}
}

// countUnresolved returns the number of threads that are genuinely outstanding:
// unresolved and not outdated. An outdated thread points at a line the author
// has already rewritten, so it no longer represents pending work.
func countUnresolved(threads []reviewThread) int {
	n := 0
	for _, t := range threads {
		if !t.Resolved && !t.Outdated {
			n++
		}
	}
	return n
}

// summarizeReviewThreads reduces threads to one event describing the
// outstanding review comments, naming who raised them so the model can weigh
// them. The timestamp is the newest unresolved thread so it sorts late in the
// trail as a current signal. Returns nil when nothing is outstanding.
func summarizeReviewThreads(threads []reviewThread) *model.Event {
	unresolved := countUnresolved(threads)
	if unresolved == 0 {
		return nil
	}
	authors := map[string]int{}
	var latest time.Time
	for _, t := range threads {
		if t.Resolved || t.Outdated {
			continue
		}
		if t.Author != "" {
			authors[t.Author]++
		}
		if t.Created.After(latest) {
			latest = t.Created
		}
	}
	if latest.IsZero() {
		latest = time.Now()
	}
	noun := "threads"
	if unresolved == 1 {
		noun = "thread"
	}
	text := fmt.Sprintf("%d unresolved review %s outstanding", unresolved, noun)
	if by := formatAuthors(authors); by != "" {
		text += " from " + by
	}
	text += " — the author has not yet resolved this feedback"
	return &model.Event{
		Timestamp: latest,
		Author:    "github-review",
		RoleHint:  "reviewer",
		Kind:      model.EventReviewThreads,
		Text:      text,
	}
}

// formatAuthors renders thread authors deterministically (sorted) with counts.
func formatAuthors(authors map[string]int) string {
	if len(authors) == 0 {
		return ""
	}
	names := make([]string, 0, len(authors))
	for n := range authors {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if authors[n] > 1 {
			parts = append(parts, fmt.Sprintf("%s (%d)", n, authors[n]))
		} else {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, ", ")
}

// summarizeReviewDecision turns GitHub's overall review decision into a trail
// event. This is an upstream verdict, so it contradicts a model guess such as
// "conditionally approved" when GitHub still says review is required. Returns
// nil when GitHub reports no decision.
func summarizeReviewDecision(decision string, at time.Time) *model.Event {
	var text string
	switch decision {
	case "APPROVED":
		text = "GitHub review decision: approved"
	case "CHANGES_REQUESTED":
		text = "GitHub review decision: changes requested"
	case "REVIEW_REQUIRED":
		text = "GitHub review decision: review still required (not yet approved)"
	default:
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	return &model.Event{
		Timestamp: at,
		Author:    "github-review",
		RoleHint:  "reviewer",
		Kind:      model.EventReviewDecision,
		Text:      text,
	}
}

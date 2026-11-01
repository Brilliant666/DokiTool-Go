package weibo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const budgetComponent = `{"code":100000,"data":{"Component_Play_Playinfo":{"mid":"123456789","title":"component survives","urls":{"1080p":"https://f.video.weibocdn.com/budget.mp4"}}}}`

func budgetTVSession(t *testing.T, optional func(context.Context) error) (*guestSession, *int) {
	t.Helper()
	calls := new(int)
	session := newGuestSession(func(ctx context.Context, address string, post *string, referer string) (unified.Object, error) {
		*calls++
		switch *calls {
		case 1:
			if post == nil {
				t.Fatal("component request lost POST body")
			}
			return unified.Object{"status": 200, "error": false, "body": budgetComponent, "url": address}, nil
		case 2:
			if post != nil || address != "https://weibo.com/ajax/statuses/show?id=123456789" {
				t.Fatal("unexpected optional status request", address)
			}
			return nil, optional(ctx)
		default:
			t.Fatal("unexpected retry after optional request failed")
			return nil, errors.New("unexpected request")
		}
	})
	return session, calls
}

func TestWeiboOptionalStatusInternalDeadlineKeepsComponent(t *testing.T) {
	parent := context.Background()
	budget, cancel := context.WithTimeout(parent, 20*time.Millisecond)
	defer cancel()
	session, calls := budgetTVSession(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	got, err := parseWithSession(parent, budget, session, &Target{Kind: "video", ID: "1034:1234567890123456"}, "https://weibo.com/tv/show/1034:1234567890123456", 1730000000)
	if err != nil || got["code"] != "OK" || *calls != 2 {
		t.Fatalf("optional deadline discarded a playable component: result=%#v calls=%d err=%v", got, *calls, err)
	}
	if !errors.Is(budget.Err(), context.DeadlineExceeded) || parent.Err() != nil {
		t.Fatal("test did not isolate the parser's internal deadline")
	}
	data := unified.Map(got["data"])
	items := unified.Slice(data["items"])
	if data["title"] != "component survives" || len(items) != 1 {
		t.Fatalf("component facts were lost: %#v", data)
	}
	selected := unified.Map(unified.Map(items[0])["default"])
	if selected["url"] != "https://f.video.weibocdn.com/budget.mp4" {
		t.Fatalf("component source was lost: %#v", selected)
	}
	if err := unified.ValidateEnvelopeAt(got, 1730000000); err != nil {
		t.Fatal(err)
	}
}

func TestWeiboOptionalStatusCallerCancellationWins(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			var parent context.Context
			var cancel context.CancelFunc
			if deadline {
				parent, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			} else {
				parent, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			budget, stop := context.WithTimeout(parent, time.Second)
			defer stop()
			session, calls := budgetTVSession(t, func(ctx context.Context) error {
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				return ctx.Err()
			})
			got, err := parseWithSession(parent, budget, session, &Target{Kind: "video", ID: "1034:1234567890123456"}, "https://weibo.com/tv/show/1034:1234567890123456", 1730000000)
			if got != nil || parent.Err() == nil || !errors.Is(err, parent.Err()) || *calls != 2 {
				t.Fatalf("caller cancellation was hidden: result=%#v calls=%d err=%v parent=%v", got, *calls, err, parent.Err())
			}
		})
	}
}

func TestWeiboMandatoryRequestInternalDeadlineStillFails(t *testing.T) {
	parent := context.Background()
	budget, cancel := context.WithTimeout(parent, 20*time.Millisecond)
	defer cancel()
	calls := 0
	session := newGuestSession(func(ctx context.Context, _ string, _ *string, _ string) (unified.Object, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	})
	got, err := parseWithSession(parent, budget, session, &Target{Kind: "status", ID: "AbC"}, "https://weibo.com/123/AbC", 1730000000)
	if got != nil || !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("mandatory timeout became success: result=%#v calls=%d err=%v", got, calls, err)
	}
}

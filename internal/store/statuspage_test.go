package store

import (
	"testing"
	"time"
)

func TestAnnouncementsAndSubscribers(t *testing.T) {
	s, c, _ := open(t)
	a, err := s.SaveAnnouncement(Announcement{Page: "public", Title: "Slow API", Body: "looking into it", Severity: "minor", Author: "ops"}, false)
	if err != nil || a.ID == 0 || a.ResolvedAt != nil {
		t.Fatalf("%+v %v", a, err)
	}
	a.Body = "fixed"
	if a, err = s.SaveAnnouncement(a, true); err != nil || a.ResolvedAt == nil || a.Body != "fixed" {
		t.Fatalf("resolve: %+v %v", a, err)
	}
	if list, _ := s.Announcements("public", c.t.Add(-time.Hour), 10); len(list) != 1 {
		t.Fatalf("recently resolved is listed: %v", list)
	}
	if list, _ := s.Announcements("public", c.t.Add(time.Hour), 10); len(list) != 0 {
		t.Fatalf("resolved long ago is not: %v", list)
	}
	if _, err := s.SaveAnnouncement(Announcement{ID: 999, Title: "x", Severity: "info"}, false); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}

	sub, err := s.Subscribe("public", " Alice@Example.com ", 2)
	if err != nil || sub.Token == "" || sub.Confirmed || sub.Email != "alice@example.com" {
		t.Fatalf("%+v %v", sub, err)
	}
	again, _ := s.Subscribe("public", "alice@example.com", 2)
	if again.Token != sub.Token {
		t.Fatal("subscribing twice keeps the token")
	}
	s.Subscribe("public", "b@example.com", 2)
	if _, err := s.Subscribe("public", "c@example.com", 2); err != ErrTooManyPending {
		t.Fatalf("pending cap: %v", err)
	}
	if list, _ := s.Subscribers("public"); len(list) != 0 {
		t.Fatal("unconfirmed are not notified")
	}
	if s.ConfirmSubscriber("other", sub.Token) == nil || s.ConfirmSubscriber("public", sub.Token) != nil {
		t.Fatal("confirm")
	}
	if list, _ := s.Subscribers("public"); len(list) != 1 || list[0].Email != "alice@example.com" {
		t.Fatalf("confirmed: %v", list)
	}
	if c, _ := s.SubscriberCounts(); c["public"] != [2]int{1, 1} {
		t.Fatalf("counts %v", c)
	}
	if s.Unsubscribe("public", sub.Token) != nil || s.Unsubscribe("public", sub.Token) != ErrNotFound {
		t.Fatal("unsubscribe")
	}
}

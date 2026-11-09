package store

import (
	"errors"
	"testing"
	"time"
)

func TestJoinCodes(t *testing.T) {
	s, c, _ := open(t)
	code, exp, err := s.CreateJoinCode(time.Hour)
	if err != nil || !exp.Equal(t0.Add(time.Hour)) {
		t.Fatal(err)
	}
	if err := s.UseJoinCode("nkj_wrong", "bj"); !errors.Is(err, ErrJoinCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if err := s.UseJoinCode(code, "bj"); err != nil {
		t.Fatal(err)
	}
	if err := s.UseJoinCode(code, "sh"); !errors.Is(err, ErrJoinCode) {
		t.Fatal("a code must work only once")
	}
	s.ReleaseJoinCode(code)
	if err := s.UseJoinCode(code, "bj"); err != nil {
		t.Fatalf("released code: %v", err)
	}
	late, _, _ := s.CreateJoinCode(time.Minute)
	c.t = c.t.Add(2 * time.Minute)
	if err := s.UseJoinCode(late, "tj"); !errors.Is(err, ErrJoinCode) {
		t.Fatal("expired code accepted")
	}
}

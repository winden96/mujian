package main

import "testing"

func TestSessionCookieOptionsFollowProductionMode(t *testing.T) {
	t.Setenv("MUJIAN_PRODUCTION", "false")
	if sessionCookieOptions().Secure {
		t.Fatal("development session cookie must remain usable over HTTP")
	}

	t.Setenv("MUJIAN_PRODUCTION", "true")
	if !sessionCookieOptions().Secure {
		t.Fatal("production session cookie must require HTTPS")
	}
}

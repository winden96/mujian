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

func TestValidateMujianStartupConfigurationFailsFastOnInvalidDefaultModel(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "unknown-model")
	t.Setenv("MUJIAN_CHAT_MODELS", "")
	if err := validateMujianStartupConfiguration(); err == nil {
		t.Fatal("invalid default model must block application startup")
	}
}

func TestValidateMujianStartupConfigurationAcceptsBuiltInDefault(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "")
	t.Setenv("MUJIAN_CHAT_MODELS", "")
	if err := validateMujianStartupConfiguration(); err != nil {
		t.Fatalf("built-in default must remain valid: %v", err)
	}
}

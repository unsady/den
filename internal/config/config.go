// Package config resolves den's paths and settings from the environment.
//
// The daemon and the CLI read the same variables, so in a container they are
// set once in the image. Outside a container the defaults live under $HOME.
package config

import (
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	// State holds the daemon's own files: jobs, run logs, and the socket.
	State string
	// Secrets is a directory of one-value files, such as telegram_token.
	Secrets string
	// Space is the agent's workspace and a git repository.
	Space string
	// Archive receives sessions exported as markdown.
	Archive string
	// Inbox receives the photos and files the user sends in the chat.
	Inbox string
	// Skills holds den's built-in skills, which the agent cannot change.
	// Empty, there are none.
	Skills string
	// FXSkills is a folder of the profile's skills where fx follows links:
	// ~/.agents/skills. fx does not follow them in ~/.fx/skills. den links
	// each built-in skill there.
	FXSkills string
	// Sessions is fx's session store.
	Sessions string
	// FX is the fx binary the daemon runs for jobs.
	FX string
	// AgentUser is the account jobs run as when the daemon runs as root.
	AgentUser string
	Socket    string
	// TranscribeURL is a speech-to-text endpoint with OpenAI's transcription
	// API. Voice messages are not transcribed when it is empty.
	TranscribeURL string
	// TranscribeModel is the model the endpoint is asked for, if it needs one.
	TranscribeModel string
	// WhisperModel is the whisper.cpp model den downloads to transcribe
	// locally, such as large-v3-turbo-q8_0. Unset, nothing is downloaded.
	WhisperModel string
	// WhisperLanguage is the spoken language, or auto to detect it.
	WhisperLanguage string
	// JobTimeout bounds one job run.
	JobTimeout time.Duration
}

func Load() Config {
	home, _ := os.UserHomeDir()
	state := env("DEN_STATE", filepath.Join(home, ".den"))

	c := Config{
		State:     state,
		Secrets:   env("DEN_SECRETS", filepath.Join(state, "secrets")),
		Space:     env("DEN_SPACE", filepath.Join(home, "space")),
		Archive:   env("DEN_ARCHIVE", filepath.Join(home, "archive")),
		Inbox:     env("DEN_INBOX", filepath.Join(home, "inbox")),
		Skills:    os.Getenv("DEN_SKILLS"),
		FXSkills:  env("DEN_FX_SKILLS", filepath.Join(home, ".agents", "skills")),
		Sessions:  env("DEN_FX_SESSIONS", filepath.Join(home, ".fx", "sessions", "v2")),
		FX:        env("DEN_FX", "fx"),
		AgentUser: os.Getenv("DEN_AGENT_USER"),
		Socket:    env("DEN_SOCKET", filepath.Join(state, "den.sock")),

		TranscribeURL:   os.Getenv("DEN_TRANSCRIBE_URL"),
		TranscribeModel: os.Getenv("DEN_TRANSCRIBE_MODEL"),
		WhisperModel:    os.Getenv("DEN_WHISPER_MODEL"),
		WhisperLanguage: env("DEN_WHISPER_LANGUAGE", "auto"),

		JobTimeout: 30 * time.Minute,
	}

	if v := os.Getenv("DEN_JOB_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.JobTimeout = d
		}
	}

	return c
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

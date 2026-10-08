package domain

import "time"

type MCPContextSource struct {
	ID            string     `json:"id"`
	OwnerUserID   string     `json:"-"`
	HomeID        string     `json:"home_id"`
	Name          string     `json:"name"`
	FileSourceID  string     `json:"file_source_id"`
	RootPath      string     `json:"root_path"`
	Enabled       bool       `json:"enabled"`
	LastTestedAt  *time.Time `json:"last_tested_at,omitempty"`
	LastTestError string     `json:"last_test_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

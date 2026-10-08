package domain

import "time"

type MonitoringSettings struct {
	HomeID        string    `json:"-"`
	InboxEnabled  bool      `json:"inbox_enabled"`
	InboxAudience string    `json:"inbox_audience"`
	EmailEnabled  bool      `json:"email_enabled"`
	SMTPHost      string    `json:"smtp_host"`
	SMTPPort      int       `json:"smtp_port"`
	SMTPUsername  string    `json:"smtp_username"`
	EmailFrom     string    `json:"email_from"`
	EmailTo       string    `json:"email_to"`
	SMTPPassword  string    `json:"-"`
	WebhookToken  string    `json:"-"`
	UpdatedAt     time.Time `json:"updated_at"`
	UpdatedBy     string    `json:"-"`
}

func DefaultMonitoringSettings(homeID string) MonitoringSettings {
	return MonitoringSettings{HomeID: homeID, InboxEnabled: true, InboxAudience: "admins", SMTPPort: 587}
}

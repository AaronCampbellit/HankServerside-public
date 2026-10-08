module github.com/dropfile/HankServerside

go 1.26.8

require (
	filippo.io/age v1.3.2
	github.com/SherClockHolmes/webpush-go v1.4.0
	github.com/cloudsoda/go-smb2 v0.0.0-20260609183447-7b96c35f5f4b
	github.com/coder/websocket v1.8.14
	github.com/coreos/go-oidc/v3 v3.17.0
	github.com/creack/pty v1.1.24
	github.com/google/jsonschema-go v0.4.3
	github.com/jackc/pgx/v5 v5.9.2
	github.com/modelcontextprotocol/go-sdk v1.7.0
	golang.org/x/crypto v0.56.0
	golang.org/x/image v0.45.0
	golang.org/x/net v0.57.0
	golang.org/x/oauth2 v0.36.0
	golang.org/x/sync v0.22.0
	golang.org/x/sys v0.47.0
)

require (
	filippo.io/hpke v0.4.0 // indirect
	github.com/geoffgarside/ber v1.1.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/hashicorp/go-uuid v1.0.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jcmturner/aescts/v2 v2.0.0 // indirect
	github.com/jcmturner/dnsutils/v2 v2.0.0 // indirect
	github.com/jcmturner/gofork v1.7.6 // indirect
	github.com/jcmturner/goidentity/v6 v6.0.1 // indirect
	github.com/jcmturner/gokrb5/v8 v8.4.4 // indirect
	github.com/jcmturner/rpc/v2 v2.0.3 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

// Preserve pinned SMB fixes while excluding unused LGPL descriptor wrappers.
replace github.com/cloudsoda/go-smb2 => ./third_party/go-smb2

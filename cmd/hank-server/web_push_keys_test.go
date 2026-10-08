package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/store"
)

func TestRunWebPushKeysCommandGeneratesVAPIDPair(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	if err := runWebPushKeysCommand(&output); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	publicKey, publicErr := base64.RawURLEncoding.DecodeString(values["HANK_WEB_PUSH_VAPID_PUBLIC_KEY"])
	privateKey, privateErr := base64.RawURLEncoding.DecodeString(values["HANK_WEB_PUSH_VAPID_PRIVATE_KEY"])
	if publicErr != nil || privateErr != nil || len(publicKey) != 65 || len(privateKey) != 32 {
		t.Fatalf("generated VAPID lengths public=%d private=%d errors=%v/%v", len(publicKey), len(privateKey), publicErr, privateErr)
	}
}

func TestSecretStorageReportIncludesWebPushSubscriptions(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	printSecretStorageReportTo(&output, store.SecretStorageReport{WebPushSubscriptions: 3})
	if !strings.Contains(output.String(), "plaintext_web_push_subscriptions=3\n") {
		t.Fatalf("secret report output = %q", output.String())
	}
}

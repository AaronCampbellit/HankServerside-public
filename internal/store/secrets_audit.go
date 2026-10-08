package store

import (
	"context"
	"errors"
)

type SecretStorageReport struct {
	MonitoringCredentials    int64
	OpenAIAccessTokens       int64
	OpenAIRefreshTokens      int64
	APNSDeviceTokens         int64
	WebPushSubscriptions     int64
	UserProfileSecretVaults  int64
	ReencryptedSecretColumns int64
}

func (r SecretStorageReport) PlaintextTotal() int64 {
	return r.MonitoringCredentials + r.OpenAIAccessTokens + r.OpenAIRefreshTokens + r.APNSDeviceTokens + r.WebPushSubscriptions + r.UserProfileSecretVaults
}

func (s *Store) SecretStorageReport(ctx context.Context) (SecretStorageReport, error) {
	var report SecretStorageReport
	checks := []struct {
		query string
		count *int64
	}{
		{`SELECT COUNT(*) FROM openai_accounts WHERE access_token <> '' AND access_token NOT LIKE ?`, &report.OpenAIAccessTokens},
		{`SELECT COUNT(*) FROM openai_accounts WHERE refresh_token <> '' AND refresh_token NOT LIKE ?`, &report.OpenAIRefreshTokens},
		{`SELECT COUNT(*) FROM apns_devices WHERE token <> '' AND token NOT LIKE ?`, &report.APNSDeviceTokens},
		{`SELECT COUNT(*) FROM web_push_subscriptions WHERE encrypted_subscription <> '' AND encrypted_subscription NOT LIKE ?`, &report.WebPushSubscriptions},
		{`SELECT COUNT(*) FROM monitoring_delivery_settings WHERE encrypted_credentials NOT LIKE ?`, &report.MonitoringCredentials},
		{`SELECT COUNT(*) FROM user_profile_secret_vaults WHERE vault_json::text <> '{}' AND vault_json::text NOT LIKE ?`, &report.UserProfileSecretVaults},
	}
	for _, check := range checks {
		pattern := encryptedSecretPrefix + "%"
		if check.query == checks[len(checks)-1].query {
			pattern = `"` + encryptedSecretPrefix + "%"
		}
		if err := s.queryRow(ctx, check.query, pattern).Scan(check.count); err != nil {
			return SecretStorageReport{}, err
		}
	}
	return report, nil
}

func (s *Store) ReencryptPlaintextSecrets(ctx context.Context) (SecretStorageReport, error) {
	if s.secretBox == nil {
		return SecretStorageReport{}, errors.New("secret encryption key is required to re-encrypt plaintext secrets")
	}
	before, err := s.SecretStorageReport(ctx)
	if err != nil {
		return SecretStorageReport{}, err
	}
	report := before
	if err := s.reencryptOpenAIAccountSecrets(ctx, &report); err != nil {
		return SecretStorageReport{}, err
	}
	if err := s.reencryptAPNSDeviceSecrets(ctx, &report); err != nil {
		return SecretStorageReport{}, err
	}
	if err := s.reencryptWebPushSubscriptionSecrets(ctx, &report); err != nil {
		return SecretStorageReport{}, err
	}
	if err := s.reencryptUserProfileSecretVaults(ctx, &report); err != nil {
		return SecretStorageReport{}, err
	}
	after, err := s.SecretStorageReport(ctx)
	if err != nil {
		return SecretStorageReport{}, err
	}
	report.OpenAIAccessTokens = after.OpenAIAccessTokens
	report.OpenAIRefreshTokens = after.OpenAIRefreshTokens
	report.APNSDeviceTokens = after.APNSDeviceTokens
	report.WebPushSubscriptions = after.WebPushSubscriptions
	report.UserProfileSecretVaults = after.UserProfileSecretVaults
	return report, nil
}

func (s *Store) reencryptWebPushSubscriptionSecrets(ctx context.Context, report *SecretStorageReport) error {
	rows, err := s.query(ctx, `SELECT id, encrypted_subscription FROM web_push_subscriptions
		WHERE encrypted_subscription <> '' AND encrypted_subscription NOT LIKE ?`, encryptedSecretPrefix+"%")
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct{ id, envelope string }
	var pending []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.envelope); err != nil {
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range pending {
		encrypted, err := s.encryptSecret(item.envelope)
		if err != nil {
			return err
		}
		if _, err := s.exec(ctx, `UPDATE web_push_subscriptions SET encrypted_subscription = ? WHERE id = ?`, encrypted, item.id); err != nil {
			return err
		}
		report.ReencryptedSecretColumns++
	}
	return nil
}

func (s *Store) reencryptOpenAIAccountSecrets(ctx context.Context, report *SecretStorageReport) error {
	rows, err := s.query(ctx, `SELECT user_id, access_token, refresh_token
		FROM openai_accounts
		WHERE (access_token <> '' AND access_token NOT LIKE ?)
		   OR (refresh_token <> '' AND refresh_token NOT LIKE ?)`, encryptedSecretPrefix+"%", encryptedSecretPrefix+"%")
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		userID       string
		accessToken  string
		refreshToken string
	}
	var rowsToUpdate []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.userID, &item.accessToken, &item.refreshToken); err != nil {
			return err
		}
		rowsToUpdate = append(rowsToUpdate, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range rowsToUpdate {
		accessToken := item.accessToken
		if accessToken != "" && !isEncryptedSecret(accessToken) {
			encrypted, err := s.encryptSecret(accessToken)
			if err != nil {
				return err
			}
			accessToken = encrypted
			report.ReencryptedSecretColumns++
		}
		refreshToken := item.refreshToken
		if refreshToken != "" && !isEncryptedSecret(refreshToken) {
			encrypted, err := s.encryptSecret(refreshToken)
			if err != nil {
				return err
			}
			refreshToken = encrypted
			report.ReencryptedSecretColumns++
		}
		if _, err := s.exec(ctx, `UPDATE openai_accounts SET access_token = ?, refresh_token = ? WHERE user_id = ?`, accessToken, refreshToken, item.userID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reencryptAPNSDeviceSecrets(ctx context.Context, report *SecretStorageReport) error {
	rows, err := s.query(ctx, `SELECT user_id, device_id, token FROM apns_devices WHERE token <> '' AND token NOT LIKE ?`, encryptedSecretPrefix+"%")
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		userID   string
		deviceID string
		token    string
	}
	var rowsToUpdate []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.userID, &item.deviceID, &item.token); err != nil {
			return err
		}
		rowsToUpdate = append(rowsToUpdate, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range rowsToUpdate {
		encrypted, err := s.encryptSecret(item.token)
		if err != nil {
			return err
		}
		if _, err := s.exec(ctx, `UPDATE apns_devices SET token = ? WHERE user_id = ? AND device_id = ?`, encrypted, item.userID, item.deviceID); err != nil {
			return err
		}
		report.ReencryptedSecretColumns++
	}
	return nil
}

func (s *Store) reencryptUserProfileSecretVaults(ctx context.Context, report *SecretStorageReport) error {
	rows, err := s.query(ctx, `SELECT user_id, vault_json::text
		FROM user_profile_secret_vaults
		WHERE vault_json::text <> '{}' AND vault_json::text NOT LIKE ?`, `"`+encryptedSecretPrefix+"%")
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		userID string
		vault  string
	}
	var rowsToUpdate []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.userID, &item.vault); err != nil {
			return err
		}
		rowsToUpdate = append(rowsToUpdate, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range rowsToUpdate {
		encrypted, err := s.encryptJSONSecret([]byte(item.vault))
		if err != nil {
			return err
		}
		if _, err := s.exec(ctx, `UPDATE user_profile_secret_vaults SET vault_json = ? WHERE user_id = ?`, encrypted, item.userID); err != nil {
			return err
		}
		report.ReencryptedSecretColumns++
	}
	return nil
}

func isEncryptedSecret(value string) bool {
	return len(value) >= len(encryptedSecretPrefix) && value[:len(encryptedSecretPrefix)] == encryptedSecretPrefix
}

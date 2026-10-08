package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/config"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/linuxrelease"
	"github.com/dropfile/HankServerside/internal/maintenance"
	"github.com/dropfile/HankServerside/internal/store"
)

func runLinuxReleaseCommand(ctx context.Context, cfg config.Cloud, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: hank-server linux-release <verify|register|activate|prune>")
	}
	command := args[0]
	if command != "verify" && command != "register" && command != "activate" && command != "prune" {
		return errors.New("usage: hank-server linux-release <verify|register|activate|prune>")
	}
	flags := flag.NewFlagSet("linux-release "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "release directory")
	manifestURL := flags.String("manifest-url", "", "public HTTPS manifest URL")
	spread := flags.Duration("spread", 15*time.Minute, "maximum rollout spread")
	root := flags.String("root", "", "managed release root")
	retain := flags.Int("retain", 3, "previous unprotected releases to retain")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("linux-release %s received unexpected arguments", command)
	}
	if command == "prune" {
		if *root == "" || *retain < 0 || *retain > 100 {
			return errors.New("linux-release prune requires --root PATH and --retain between 0 and 100")
		}
		return pruneLinuxReleases(ctx, cfg, *root, *retain, output)
	}
	if *dir == "" {
		return fmt.Errorf("linux-release %s requires an explicit --dir PATH", command)
	}
	release, err := linuxrelease.Load(*dir, cfg.LinuxAgentReleasePublicKey)
	if err != nil {
		return err
	}
	manifestDigest := sha256.Sum256(release.Manifest())
	digest := hex.EncodeToString(manifestDigest[:])
	if command == "verify" {
		_, err = fmt.Fprintf(output, "version=%s\nsource_commit=%s\nmanifest_sha256=%s\n", release.Version(), release.SourceCommit(), digest)
		return err
	}
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	now := time.Now().UTC()
	if command == "register" {
		parsed, parseErr := url.Parse(strings.TrimSpace(*manifestURL))
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return errors.New("linux-release register requires an HTTPS --manifest-url")
		}
		err = db.RegisterLinuxAgentRelease(ctx, domain.LinuxAgentRelease{Version: release.Version(), ManifestSHA256: digest, SourceCommit: release.SourceCommit(), ManifestURL: parsed.String(), State: "hosted", CreatedAt: now})
		if err == nil {
			_, err = fmt.Fprintf(output, "registered version=%s manifest_sha256=%s\n", release.Version(), digest)
		}
		return err
	}
	if *spread < 0 || *spread > 15*time.Minute {
		return errors.New("linux-release activate spread must be between zero and 15m")
	}
	registered, err := db.GetLinuxAgentRelease(ctx, release.Version())
	if err != nil {
		return fmt.Errorf("release must be registered before activation: %w", err)
	}
	if registered.ManifestSHA256 != digest {
		return errors.New("hosted release digest does not match registered release")
	}
	rollout, err := db.ActivateLinuxAgentRollout(ctx, release.Version(), now, *spread)
	if err == nil {
		_, err = fmt.Fprintf(output, "activated version=%s rollout_id=%s scope=%s\n", rollout.Version, rollout.ID, rollout.Scope)
	}
	return err
}

func pruneLinuxReleases(ctx context.Context, cfg config.Cloud, root string, retain int, output io.Writer) error {
	root, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || root == "/" {
		return errors.New("invalid Linux release root")
	}
	active, err := linuxrelease.Load(filepath.Join(root, "current"), cfg.LinuxAgentReleasePublicKey)
	if err != nil {
		return fmt.Errorf("verify active Linux release before prune: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	protected, err := db.ListProtectedLinuxAgentVersions(ctx)
	if err != nil {
		return err
	}
	prunable := maintenance.PrunableLinuxVersions(versions, active.Version(), retain, protected)
	if err := maintenance.RemoveLinuxVersionDirectories(root, prunable); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "active=%s\npruned=%s\n", active.Version(), strings.Join(prunable, ","))
	return err
}

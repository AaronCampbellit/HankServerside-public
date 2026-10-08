# Hank SMB dependency scope

This is BSD-2-Clause CloudSoda/go-smb2 at commit `7b96c35f5f4b`
(`v0.0.0-20260609183447-7b96c35f5f4b`), with its [LICENSE](LICENSE) preserved.
Original Hank application code keeps its separate rights-reserved terms; this
third-party component retains BSD terms.

Hank uses SMB authentication, sessions/shares, listing, stat, reads, writes,
rename, directory creation and deletion. It does not expose parsed Windows
security descriptors or enriched directory listings. The [patch](HANK-PATCH.patch)
removes the unused `sddl`-based encoder, `Share/File.SecurityInfo` and
`SetSecurityInfo` parsed wrappers, `Share.ReadDirPlus`, `File.ReaddirPlus`, and
`DirEntryPlus`, and their two integration tests. Raw security-information
methods remain unchanged. The module minimum Go, crypto/net, and test dependency
versions match the reviewed application graph so standalone fork tests do not
fall back to older crypto versions.

Transport, authentication, negotiation, signing/encryption, path validation,
reparse handling, credit/compound-request behavior, ordinary file/directory
APIs, and their tests retain the exact upstream implementations. Only
`client.go`, `smb2_test.go`, `go.mod`, and `go.sum` differ; the
[per-file digests](HANK-PROVENANCE.json) record that comparison. This removes the
LGPL-licensed `cloudsoda/sddl` dependency without reverting the pinned SMB fixes
or granting broader permissions to original application code.

To reproduce, copy the exact checksum-verified upstream Go module to a writable
temporary directory. From there run
`git apply --check /absolute/path/to/HANK-PATCH.patch`, then
`git apply /absolute/path/to/HANK-PATCH.patch`. The patch contains only the four
upstream-file changes; these provenance files and local regression tests are
additional files.

When upgrading, review upstream security fixes and licenses, refresh this
bounded patch/digests, run upstream tests and Hank's file security tests, and
verify the production import/binary graph still excludes `cloudsoda/sddl`.
Do not replace this source with an older SMB release merely to avoid its former
transitive dependency. The upstream README describes the full original API;
removed parsed-descriptor APIs are intentionally unavailable in this fork.

Upstream source: <https://github.com/cloudsoda/go-smb2/tree/7b96c35f5f4b>.

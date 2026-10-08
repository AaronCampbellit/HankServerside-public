export type SMBShare = {
  id: string;
  name: string;
  host: string;
  share: string;
  domain: string;
  username: string;
  password_set?: boolean;
  policy?: Record<string, unknown>;
};

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

export function cleanSMBID(value: string): string {
  return value.trim().toLowerCase().replace(/[^a-z0-9_.-]+/g, "-").replace(/^-+|-+$/g, "");
}

export function normalizeSMBHost(value: string): string {
  let host = value.trim();
  host = host.replace(/^[a-z][a-z0-9+.-]*:\/\//i, "");
  host = host.replace(/^\\\\+/, "");
  host = host.replace(/^\/+/, "");
  return (host.split(/[/?#]/)[0] || host).trim();
}

function sourceRecords(config: Record<string, unknown>): Array<Record<string, unknown>> {
  for (const candidate of [config.shares, config.file_sources, config.sources]) {
    if (!Array.isArray(candidate)) continue;
    const records = candidate.flatMap((entry) => {
      if (!entry || typeof entry !== "object") return [];
      const record = entry as Record<string, unknown>;
      if (record.type && record.type !== "smb") return [];
      return [{ ...record }];
    });
    if (records.length > 0) return records;
  }
  return firstString(config.host, config.smb_host, config.share, config.smb_share) ? [{ ...config }] : [];
}

function shareFromRecord(record: Record<string, unknown>, index: number): SMBShare {
  const share = firstString(record.share, record.smb_share);
  const id = cleanSMBID(firstString(record.id, record.source_id, record.name, share, `smb-${index + 1}`)) || `smb-${index + 1}`;
  return {
    id,
    name: firstString(record.name, share, "SMB Share"),
    host: firstString(record.host, record.smb_host),
    share,
    domain: firstString(record.domain, record.smb_domain),
    username: firstString(record.username, record.smb_username),
    password_set: Boolean(record.password_set || record.smb_password_set),
    ...(record.policy && typeof record.policy === "object" ? { policy: { ...(record.policy as Record<string, unknown>) } } : {}),
  };
}

export function smbSourceRecords(config: Record<string, unknown>): SMBShare[] {
  return sourceRecords(config).map(shareFromRecord);
}

export function newShareDraft(existing: SMBShare[]): SMBShare {
  const ids = new Set(existing.map((share) => share.id));
  let index = 1;
  while (ids.has(`smb-${index}`)) index += 1;
  return { id: `smb-${index}`, name: "", host: "", share: "", domain: "", username: "" };
}

function publicShare(share: SMBShare, existing: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    ...existing,
    id: share.id,
    name: share.name,
    host: share.host,
    share: share.share,
    domain: share.domain,
    username: share.username,
    ...(share.policy ? { policy: share.policy } : {}),
  };
}

function mirrorActiveShare(config: Record<string, unknown>, shares: Array<Record<string, unknown>>, requestedActiveID: string): Record<string, unknown> {
  const active = shares.find((record) => firstString(record.id, record.source_id) === requestedActiveID) || shares[0];
  if (!active) {
    return { ...config, active_source_id: "", host: "", share: "", domain: "", username: "" };
  }
  const activeShare = shareFromRecord(active, 0);
  return {
    ...config,
    active_source_id: activeShare.id,
    host: activeShare.host,
    share: activeShare.share,
    domain: activeShare.domain,
    username: activeShare.username,
  };
}

export function upsertSMBShare(
  config: Record<string, unknown>,
  share: SMBShare,
  originalID = "",
): Record<string, unknown> {
  const records = sourceRecords(config);
  const matchID = originalID || share.id;
  const index = records.findIndex((record) => cleanSMBID(firstString(record.id, record.source_id)) === matchID);
  const nextRecords = [...records];
  if (index >= 0) {
    nextRecords[index] = publicShare(share, records[index]);
  } else {
    nextRecords.push(publicShare(share));
  }
  const currentActiveID = firstString(config.active_source_id) || (records[0] ? shareFromRecord(records[0], 0).id : "");
  const activeID = currentActiveID === originalID && originalID !== share.id ? share.id : currentActiveID || share.id;
  return mirrorActiveShare({ ...config, shares: nextRecords }, nextRecords, activeID);
}

export function removeSMBShare(config: Record<string, unknown>, shareID: string): Record<string, unknown> {
  const records = sourceRecords(config);
  const nextRecords = records.filter((record) => cleanSMBID(firstString(record.id, record.source_id)) !== shareID);
  const currentActiveID = firstString(config.active_source_id) || (records[0] ? shareFromRecord(records[0], 0).id : "");
  const activeID = currentActiveID === shareID ? firstString(nextRecords[0]?.id, nextRecords[0]?.source_id) : currentActiveID;
  return mirrorActiveShare({ ...config, shares: nextRecords }, nextRecords, activeID);
}

export function validateSMBShare(share: SMBShare, existing: SMBShare[], originalID = ""): string {
  if (!cleanSMBID(share.id)) return "Share ID is required.";
  if (existing.some((candidate) => candidate.id === cleanSMBID(share.id) && candidate.id !== originalID)) return "Share ID is already in use.";
  if (!share.name.trim()) return "Share label is required.";
  if (!normalizeSMBHost(share.host)) return "Server address is required.";
  if (!share.share.trim()) return "Share name is required.";
  return "";
}

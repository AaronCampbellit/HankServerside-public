import { apiClient, type ApiTransport } from "./client";
import { arrayFrom, booleanFrom } from "./normalize";

export type RecoveryBundle = Record<string, unknown>;

export type RecoveryChange = {
  area?: string;
  target?: string;
  action?: string;
};

export type RecoveryRequiredSecret = {
  id?: string;
  label?: string;
  service_type?: string;
  kind?: string;
  target?: { field?: string } & Record<string, unknown>;
};

export type RecoveryPreview = {
  valid?: boolean;
  changes?: RecoveryChange[];
  required_secrets?: RecoveryRequiredSecret[];
  warnings?: string[];
};

export type RecoveryApplyResult = {
  applied?: boolean;
  changes?: RecoveryChange[];
  required_secrets?: RecoveryRequiredSecret[];
};

export class RecoveryClient {
  constructor(private readonly api: ApiTransport = apiClient) {}

  exportBundle() {
    return this.api.request<RecoveryBundle>("/v1/home/recovery/export");
  }

  async previewImport(bundle: RecoveryBundle): Promise<RecoveryPreview> {
    const payload = await this.api.request<RecoveryPreview>("/v1/home/recovery/import/preview", {
      method: "POST",
      body: bundle,
    });
    return normalizeRecoveryPreview(payload);
  }

  async applyImport(bundle: RecoveryBundle): Promise<RecoveryApplyResult> {
    const payload = await this.api.request<RecoveryApplyResult>("/v1/home/recovery/import/apply", {
      method: "POST",
      body: { bundle, confirm: true },
    });
    return {
      applied: booleanFrom(payload.applied),
      changes: arrayFrom<RecoveryChange>(payload.changes),
      required_secrets: arrayFrom<RecoveryRequiredSecret>(payload.required_secrets),
    };
  }
}

function normalizeRecoveryPreview(payload: RecoveryPreview): RecoveryPreview {
  return {
    ...payload,
    valid: booleanFrom(payload.valid),
    changes: arrayFrom<RecoveryChange>(payload.changes),
    required_secrets: arrayFrom<RecoveryRequiredSecret>(payload.required_secrets),
    warnings: arrayFrom<string>(payload.warnings),
  };
}

export const recoveryClient = new RecoveryClient();

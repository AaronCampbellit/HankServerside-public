import { apiClient, type ApiTransport } from "./client";
import { entityName, homeAssistantClient, type HomeAssistantClient, type HomeAssistantEntity } from "./homeAssistant";
import { arrayFrom } from "./normalize";

export type SearchResultType = "page" | "note" | "kanban_card" | "machine" | "quick_link" | "app" | "member" | "file" | "homeassistant";

export type SearchResult = {
  type: SearchResultType | string;
  title: string;
  subtitle?: string;
  url: string;
  external?: boolean;
};

export type SearchResponse = {
  query: string;
  results: SearchResult[];
  file_index_status?: "ready" | "indexing" | "partial" | "offline";
};

export type SearchOutput = {
  results: SearchResult[];
  fileIndexStatus?: SearchResponse["file_index_status"];
};

const searchSourceTimeoutMs = 3000;

export class SearchClient {
  private statesCache?: { promise: Promise<HomeAssistantEntity[]>; expiresAt: number };

  constructor(
    private readonly api: ApiTransport = apiClient,
    private readonly homeAssistant: HomeAssistantClient = homeAssistantClient,
  ) {}

  async search(query: string, signal?: AbortSignal): Promise<SearchOutput> {
    const q = query.trim();
    if (!q) return { results: [] };
    const [cloudResults, agentResults] = await Promise.allSettled([
      this.searchCloud(q, signal),
      this.searchAgent(q),
    ]);
    if (cloudResults.status === "rejected" && agentResults.status === "rejected") {
      throw new Error("Search is unavailable.");
    }
    const results = uniqueSearchResults([
      ...(cloudResults.status === "fulfilled" ? cloudResults.value.results : []),
      ...(agentResults.status === "fulfilled" ? agentResults.value : []),
    ])
      .sort((left, right) => {
        const groupOrder = searchResultGroup(left) - searchResultGroup(right);
        return groupOrder || searchResultScore(right, q) - searchResultScore(left, q);
      })
      .slice(0, 24);
    return { results, fileIndexStatus: cloudResults.status === "fulfilled" ? cloudResults.value.file_index_status : "offline" };
  }

  private async searchCloud(query: string, signal?: AbortSignal): Promise<SearchResponse> {
    const payload = await this.api.request<SearchResponse>(
      `/v1/home/search?q=${encodeURIComponent(query)}`,
      { signal, timeoutMs: searchSourceTimeoutMs },
    );
    return { query, results: arrayFrom<SearchResult>(payload?.results), file_index_status: payload?.file_index_status };
  }

  private async searchAgent(query: string): Promise<SearchResult[]> {
    const entities = await Promise.allSettled([
      withSearchTimeout(this.cachedHomeAssistantStates()),
    ]);
    if (entities[0].status === "rejected") {
      throw new Error("Agent search is unavailable.");
    }
    return homeAssistantSearchResults(entities[0].value, query);
  }

  private cachedHomeAssistantStates(): Promise<HomeAssistantEntity[]> {
    if (this.statesCache && Date.now() < this.statesCache.expiresAt) return this.statesCache.promise;
    const promise = this.homeAssistant.fetchStates();
    this.statesCache = { promise, expiresAt: Date.now() + 5000 };
    void promise.catch(() => { if (this.statesCache?.promise === promise) this.statesCache = undefined; });
    return promise;
  }
}

function withSearchTimeout<T>(request: Promise<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timeoutID = window.setTimeout(() => reject(new Error("Search source timed out.")), searchSourceTimeoutMs);
    request.then(
      (value) => {
        window.clearTimeout(timeoutID);
        resolve(value);
      },
      (error) => {
        window.clearTimeout(timeoutID);
        reject(error);
      },
    );
  });
}

export const searchClient = new SearchClient();

function homeAssistantSearchResults(entities: HomeAssistantEntity[], query: string): SearchResult[] {
  const needle = query.toLowerCase();
  return arrayFrom<HomeAssistantEntity>(entities)
    .filter((entity) => homeAssistantText(entity).includes(needle))
    .slice(0, 8)
    .map((entity) => ({
      type: "homeassistant",
      title: entityName(entity),
      subtitle: `${entity.entity_id} · ${entity.state}`,
      url: `/dashboard/home-assistant?query=${encodeURIComponent(entity.entity_id)}`,
    }));
}

function homeAssistantText(entity: HomeAssistantEntity): string {
  const attrs = entity.attributes || {};
  return [
    entity.entity_id,
    entity.state,
    attrs.friendly_name,
    attrs.device_class,
    attrs.area_id,
    attrs.unit_of_measurement,
  ].filter(Boolean).join(" ").toLowerCase();
}

function uniqueSearchResults(results: SearchResult[]): SearchResult[] {
  const seen = new Set<string>();
  const unique: SearchResult[] = [];
  for (const result of results) {
    const key = `${result.type}:${result.url}:${result.title}`;
    if (seen.has(key)) continue;
    seen.add(key);
    unique.push(result);
  }
  return unique;
}

function searchResultScore(result: SearchResult, query: string): number {
  const needle = query.trim().toLowerCase();
  const title = result.title.toLowerCase();
  const subtitle = (result.subtitle || "").toLowerCase();
  const kindBonus: Record<string, number> = {
    page: 40,
    note: 30,
    homeassistant: 20,
    file: 10,
  };
  const matchScore = title === needle
    ? 1000
    : title.startsWith(needle)
      ? 800
      : title.includes(needle)
        ? 600
        : subtitle.startsWith(needle)
          ? 400
          : subtitle.includes(needle)
            ? 200
            : 0;
  return matchScore + (kindBonus[result.type] || 0);
}

function searchResultGroup(result: SearchResult): number {
  if (result.url.startsWith("/docs/") || result.subtitle?.trim().toLowerCase() === "docs") return 1;
  switch (result.type) {
    case "page":
    case "quick_link":
    case "app":
    case "member":
      return 0;
    case "note":
	case "kanban_card":
      return 2;
    case "file":
      return 3;
    case "homeassistant":
      return 4;
    default:
      return 5;
  }
}

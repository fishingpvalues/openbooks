import { createApi, fetchBaseQuery } from "@reduxjs/toolkit/query/react";
import { getToken } from "./token";
import { getApiURL } from "./util";
import { BookDetail } from "./messages";

export interface IrcServer {
  elevatedUsers?: string[];
  regularUsers?: string[];
}

export interface Book {
  name: string;
  downloadLink: string;
  time: string;
}

// v5.3.0 acquisition layer types (server/openapi.json). The server is the
// source of truth; these mirror the JSON tags in server/wanted.go,
// server/api.go (DownloadJob) and server/searchcache.go (QualityFilters).

// QualityFilters are the quality controls for a search; empty = no filter.
// Persisted on wanted entries so poll rounds re-search the same shape.
export interface QualityFilters {
  formats?: string[];
  language?: string;
  maxSizeBytes?: number;
  prefer?: "ebook" | "audiobook";
}

export interface WantedItem {
  query: string;
  author?: string;
  autoFetch: boolean;
  addedAt: string;
  fetchedAt?: string;
  matchedAt?: string;
  matched?: BookDetail[];
  filters?: QualityFilters;
  withSidecar?: boolean;
  attempts: number;
  failedRounds?: number;
  lastAttemptAt?: string;
  nextAttemptAt?: string;
  seenReleases?: Record<string, boolean>;
  staleSince?: string;
}

export interface WantedAddRequest {
  query: string;
  author?: string;
  autoFetch?: boolean;
  filters?: QualityFilters;
  withSidecar?: boolean;
}

// Every download requested through POST /download, newest first, 256 cap.
export interface DownloadJob {
  id: string;
  book: string;
  status: "requested" | "downloading" | "completed" | "failed";
  requestedAt: string;
  completedAt?: string;
  retries: number;
  fileName?: string;
  libraryPath?: string;
  sha256?: string;
  withSidecar?: boolean;
  failedDetail?: string;
}

export interface SearchCacheStats {
  entries: number;
  hits: number;
  misses: number;
  ttl: string;
}

// v5.2.0: the unified multi-source search. Results are normalized across
// legs: the IRC leg identifies a fetch with BookID (the "!"-prefixed DCC
// line), the Prowlarr leg with URL (magnet or download link).
export interface UnifiedResult {
  source: string;
  title: string;
  author?: string;
  format?: string;
  size?: string;
  url?: string;
  bookId?: string;
  dedupGroup?: number;
}

export interface UnifiedSourceStatus {
  source: string;
  status: string;
  note?: string;
  hits: number;
}

export interface UnifiedSearchResponse {
  query: string;
  results: UnifiedResult[];
  sources: UnifiedSourceStatus[];
  took: string;
}

export interface UnifiedSearchRequest {
  query: string;
  sources?: string[];
  filters?: QualityFilters;
}

// v5.3.0: sha256 verify contract (POST /api/v1/verify).
export interface VerifyResponse {
  fileName: string;
  status: "ok" | "mismatch" | "missing";
  sha256?: string;
  recorded?: string;
  detail?: string;
}

export const openbooksApi = createApi({
  baseQuery: fetchBaseQuery({
    baseUrl: getApiURL().href,
    credentials: "include",
    mode: "cors",
    prepareHeaders: (headers) => {
      const t = getToken();
      if (t) {
        headers.set("Authorization", `Bearer ${t}`);
      }
      return headers;
    }
  }),
  tagTypes: ["books", "servers", "wanted", "jobs", "searchCache"],
  endpoints: (builder) => ({
    getHealth: builder.query<
      { name: string; version: string; persist: boolean },
      null
    >({
      query: () => "api/v1/health"
    }),
    getServers: builder.query<string[], null>({
      query: () => `servers`,
      transformResponse: (ircServers: IrcServer) => {
        return ircServers.elevatedUsers ?? [];
      }
    }),
    getBooks: builder.query<Book[], null>({
      query: () => `library`,
      providesTags: ["books"]
    }),
    deleteBook: builder.mutation<null, string>({
      query: (book) => ({
        url: `library/${book}`,
        method: "DELETE"
      }),
      invalidatesTags: ["books"]
    }),
    // v5.3.0: the wanted watchlist (re-search poller, v5.2.0) with the
    // lifecycle state from v5.3.0.
    getWanted: builder.query<WantedItem[], null>({
      query: () => `api/v1/wanted`,
      providesTags: ["wanted"]
    }),
    addWanted: builder.mutation<WantedItem, WantedAddRequest>({
      query: (body) => ({
        url: `api/v1/wanted`,
        method: "POST",
        body
      }),
      invalidatesTags: ["wanted"]
    }),
    deleteWanted: builder.mutation<null, string>({
      query: (query) => ({
        // The server unescapes the segment (server/wanted.go
        // wantedDeleteHandler); queries contain spaces and punctuation.
        url: `api/v1/wanted/${encodeURIComponent(query)}`,
        method: "DELETE"
      }),
      invalidatesTags: ["wanted"]
    }),
    // v5.3.0: per-job download tracking + retry.
    getJobs: builder.query<DownloadJob[], null>({
      query: () => `api/v1/jobs`,
      providesTags: ["jobs"]
    }),
    retryJob: builder.mutation<{ status: string; detail: string }, string>({
      query: (id) => ({
        url: `api/v1/jobs/${id}/retry`,
        method: "POST"
      }),
      invalidatesTags: ["jobs"]
    }),
    // v5.3.0: search cache stats + clean.
    getSearchCache: builder.query<SearchCacheStats, null>({
      query: () => `api/v1/search-cache`,
      providesTags: ["searchCache"]
    }),
    cleanSearchCache: builder.mutation<{ removed: number }, void>({
      query: () => ({
        url: `api/v1/search-cache/clean`,
        method: "POST"
      }),
      invalidatesTags: ["searchCache"]
    }),
    // v5.2.0: the unified multi-source search (IRC + Prowlarr in one call).
    unifiedSearch: builder.mutation<
      UnifiedSearchResponse,
      UnifiedSearchRequest
    >({
      query: (body) => ({
        url: `api/v1/search/unified`,
        method: "POST",
        body
      })
    }),
    // v5.3.0: sha256 verify of a landed file (recompute=true establishes
    // the baseline when nothing is recorded yet).
    verifyBook: builder.mutation<
      VerifyResponse,
      { fileName: string; recompute?: boolean }
    >({
      query: (body) => ({
        url: `api/v1/verify`,
        method: "POST",
        body
      }),
      invalidatesTags: ["jobs"]
    })
  })
});

export const {
  useGetHealthQuery,
  useGetServersQuery,
  useGetBooksQuery,
  useDeleteBookMutation,
  useGetWantedQuery,
  useAddWantedMutation,
  useDeleteWantedMutation,
  useGetJobsQuery,
  useRetryJobMutation,
  useGetSearchCacheQuery,
  useCleanSearchCacheMutation,
  useUnifiedSearchMutation,
  useVerifyBookMutation
} = openbooksApi;

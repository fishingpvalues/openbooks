import { createApi, fetchBaseQuery } from "@reduxjs/toolkit/query/react";
import { getToken } from "./token";
import { getApiURL } from "./util";

export interface IrcServer {
  elevatedUsers?: string[];
  regularUsers?: string[];
}

export interface Book {
  name: string;
  downloadLink: string;
  time: string;
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
  tagTypes: ["books", "servers"],
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
    })
  })
});

export const {
  useGetHealthQuery,
  useGetServersQuery,
  useGetBooksQuery,
  useDeleteBookMutation
} = openbooksApi;

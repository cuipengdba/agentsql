import { request } from "./client";
import type { DatasourceInput, DatasourceView, DeleteView, PageQuery, PageResp, PingView } from "./types";

export function listDatasources(params: PageQuery = {}, signal?: AbortSignal): Promise<PageResp<DatasourceView>> {
  return request<PageResp<DatasourceView>>({ method: "GET", url: "/datasources", params, signal });
}

export function getDatasource(id: string): Promise<DatasourceView> {
  return request<DatasourceView>({ method: "GET", url: `/datasources/${encodeURIComponent(id)}` });
}

export function createDatasource(input: DatasourceInput): Promise<DatasourceView> {
  return request<DatasourceView>({ method: "POST", url: "/datasources", data: input });
}

export function updateDatasource(id: string, input: DatasourceInput): Promise<DatasourceView> {
  return request<DatasourceView>({ method: "PUT", url: `/datasources/${encodeURIComponent(id)}`, data: input });
}

export function deleteDatasource(id: string): Promise<DeleteView> {
  return request<DeleteView>({ method: "DELETE", url: `/datasources/${encodeURIComponent(id)}` });
}

export function pingDatasource(id: string): Promise<PingView> {
  return request<PingView>({ method: "POST", url: `/datasources/${encodeURIComponent(id)}/ping` });
}

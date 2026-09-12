import { request } from "./client";
import type { DeleteView, MaskRuleInput, MaskRuleView, PageQuery, PageResp } from "./types";

export interface MaskRuleQuery extends PageQuery {
  datasource_id?: string;
}

export function listMaskRules(params: MaskRuleQuery = {}): Promise<PageResp<MaskRuleView>> {
  return request<PageResp<MaskRuleView>>({ method: "GET", url: "/mask_rules", params });
}

export function createMaskRule(input: MaskRuleInput): Promise<MaskRuleView> {
  return request<MaskRuleView>({ method: "POST", url: "/mask_rules", data: input });
}

export function updateMaskRule(id: string, input: MaskRuleInput): Promise<MaskRuleView> {
  return request<MaskRuleView>({ method: "PUT", url: `/mask_rules/${encodeURIComponent(id)}`, data: input });
}

export function deleteMaskRule(id: string): Promise<DeleteView> {
  return request<DeleteView>({ method: "DELETE", url: `/mask_rules/${encodeURIComponent(id)}` });
}

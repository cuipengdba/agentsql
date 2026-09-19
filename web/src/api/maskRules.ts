import { request } from "./client";
import type { DeleteView, MaskRuleInput, MaskRuleView, PageQuery, PageResp } from "./types";

export interface MaskRuleQuery extends PageQuery {
  datasource_id?: string;
}

// 脱敏规则页对内联加载失败、表单 503/422、启停冲突与删除失败都有自己的中文展示，
// 统一抑制客户端全局英文 toast，避免同一错误弹出两条提示。
export function listMaskRules(params: MaskRuleQuery = {}, signal?: AbortSignal): Promise<PageResp<MaskRuleView>> {
  return request<PageResp<MaskRuleView>>({
    method: "GET",
    url: "/mask_rules",
    params,
    signal,
    suppressErrorMessage: true,
  });
}

export function createMaskRule(input: MaskRuleInput): Promise<MaskRuleView> {
  return request<MaskRuleView>({
    method: "POST",
    url: "/mask_rules",
    data: input,
    suppressErrorMessage: true,
  });
}

export function updateMaskRule(id: string, input: MaskRuleInput): Promise<MaskRuleView> {
  return request<MaskRuleView>({
    method: "PUT",
    url: `/mask_rules/${encodeURIComponent(id)}`,
    data: input,
    suppressErrorMessage: true,
  });
}

export function deleteMaskRule(id: string): Promise<DeleteView> {
  return request<DeleteView>({
    method: "DELETE",
    url: `/mask_rules/${encodeURIComponent(id)}`,
    suppressErrorMessage: true,
  });
}

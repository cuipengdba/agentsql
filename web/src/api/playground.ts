import { request } from "./client";
import type { PlaygroundAssessRequest, PlaygroundAssessView } from "./types";

export function assessPlayground(body: PlaygroundAssessRequest, signal?: AbortSignal): Promise<PlaygroundAssessView> {
  return request<PlaygroundAssessView>({ method: "POST", url: "/playground/assess", data: body, signal });
}

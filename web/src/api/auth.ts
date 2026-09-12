import { request } from "./client";
import type { AdminView, LoginInput, LoginView } from "./types";

export function login(input: LoginInput): Promise<LoginView> {
  return request<LoginView>({ method: "POST", url: "/auth/login", data: input });
}

export function me(): Promise<AdminView> {
  return request<AdminView>({ method: "GET", url: "/auth/me" });
}

export function logout(): Promise<{ ok: boolean }> {
  return request<{ ok: boolean }>({ method: "POST", url: "/auth/logout" });
}

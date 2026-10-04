import { request } from "./client";
import type { AdminView, HumanAuthConfig, LoginInput, LoginView } from "./types";

export function login(input: LoginInput): Promise<LoginView> {
  return request<LoginView>({ method: "POST", url: "/auth/login", data: input });
}

export function ldapLogin(input: LoginInput): Promise<LoginView> {
  return request<LoginView>({ method: "POST", url: "/auth/ldap/login", data: input });
}

export function authConfig(): Promise<HumanAuthConfig> {
  return request<HumanAuthConfig>({ method: "GET", url: "/auth/config", suppressErrorMessage: true });
}

export function verifyMFA(input: { challenge_token: string; code?: string; recovery_code?: string }): Promise<LoginView> {
  return request<LoginView>({ method: "POST", url: "/auth/mfa/verify", data: input });
}

export interface MFAStatus { enabled: boolean; pending: boolean }
export interface MFAEnrollment { secret: string; otpauth_uri: string; recovery_codes: string[] }

export function mfaStatus(): Promise<MFAStatus> {
  return request<MFAStatus>({ method: "GET", url: "/auth/mfa" });
}

export function enrollMFA(): Promise<MFAEnrollment> {
  return request<MFAEnrollment>({ method: "POST", url: "/auth/mfa/enroll" });
}

export function confirmMFA(code: string): Promise<{ enabled: boolean }> {
  return request<{ enabled: boolean }>({ method: "POST", url: "/auth/mfa/confirm", data: { code } });
}

export function disableMFA(input: { code?: string; recovery_code?: string }): Promise<{ enabled: boolean }> {
  return request<{ enabled: boolean }>({ method: "DELETE", url: "/auth/mfa", data: input });
}

export function me(): Promise<AdminView> {
  return request<AdminView>({ method: "GET", url: "/auth/me" });
}

export function logout(): Promise<{ ok: boolean }> {
  return request<{ ok: boolean }>({ method: "POST", url: "/auth/logout" });
}

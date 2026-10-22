import { create } from "zustand";

export type AppLocale = "zh-CN" | "en-US";

const STORAGE_KEY = "agentsql.locale";

function initialLocale(): AppLocale {
  if (typeof window === "undefined") return "zh-CN";
  const params = new URLSearchParams(window.location.search);
  const requested = params.get("lang");
  const linkedLocale = requested === "en" || requested === "en-US" ? "en-US"
    : requested === "zh" || requested === "zh-CN" ? "zh-CN" : null;
  if (linkedLocale) {
    try { window.localStorage.setItem(STORAGE_KEY, linkedLocale); } catch { /* Storage may be unavailable. */ }
    params.delete("lang");
    const query = params.toString();
    window.history.replaceState(window.history.state, "", window.location.pathname + (query ? `?${query}` : "") + window.location.hash);
    return linkedLocale;
  }
  try {
    return window.localStorage.getItem(STORAGE_KEY) === "en-US" ? "en-US" : "zh-CN";
  } catch {
    return "zh-CN";
  }
}

interface LocaleState {
  locale: AppLocale;
  setLocale: (locale: AppLocale) => void;
}

export const useLocaleStore = create<LocaleState>((set) => ({
  locale: initialLocale(),
  setLocale: (locale) => {
    if (typeof window !== "undefined") {
      try { window.localStorage.setItem(STORAGE_KEY, locale); } catch { /* Private browsing can disable storage. */ }
    }
    set({ locale });
  },
}));

export function currentLocale(): AppLocale {
  return useLocaleStore.getState().locale;
}

export function localeText(zh: string, en: string, locale: AppLocale): string {
  return locale === "en-US" ? en : zh;
}

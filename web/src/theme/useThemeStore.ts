import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";

import type { ThemeMode } from "./tokens";

interface ThemeState {
  mode: ThemeMode;
  setMode: (mode: ThemeMode) => void;
  toggle: () => void;
}

export const useThemeStore = create<ThemeState>()(
  persist(
    (set) => ({
      mode: "dark",
      setMode: (mode) => set({ mode }),
      toggle: () => set((state) => ({ mode: state.mode === "dark" ? "light" : "dark" })),
    }),
    {
      name: "agentsql.theme",
      storage: createJSONStorage(() => localStorage),
      partialize: ({ mode }) => ({ mode }),
    },
  ),
);

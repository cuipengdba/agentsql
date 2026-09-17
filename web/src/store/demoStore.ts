import { create } from "zustand";

import { fetchDemoProjection } from "@/api/health";

interface DemoState {
  enabled: boolean;
  banner: string;
  loaded: boolean;
  load: () => Promise<void>;
}

let pendingLoad: Promise<void> | null = null;

export const useDemoStore = create<DemoState>()((set, get) => ({
  enabled: false,
  banner: "",
  loaded: false,
  load: async () => {
    if (get().loaded) return;
    if (pendingLoad) return pendingLoad;

    pendingLoad = fetchDemoProjection()
      .then((demo) => {
        set({ enabled: demo !== null, banner: demo?.banner ?? "", loaded: true });
      })
      .finally(() => {
        pendingLoad = null;
      });
    return pendingLoad;
  },
}));

export const selectIsDemo = (state: DemoState): boolean => state.enabled;

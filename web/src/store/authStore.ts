import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";

interface AuthSession {
  token: string;
  expiresAt: string;
  username: string;
}

interface AuthState {
  token: string | null;
  expiresAt: string | null;
  username: string | null;
  isAuthenticated: () => boolean;
  login: (session: AuthSession) => void;
  setUsername: (username: string) => void;
  logout: () => void;
  clear: () => void;
}

const emptySession = {
  token: null,
  expiresAt: null,
  username: null,
};

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => ({
      ...emptySession,
      isAuthenticated: () => {
        const { token, expiresAt } = get();
        if (!token || !expiresAt) {
          return false;
        }
        const expiresAtTime = Date.parse(expiresAt);
        return Number.isFinite(expiresAtTime) && expiresAtTime > Date.now();
      },
      login: (session) => set(session),
      setUsername: (username) => set({ username }),
      logout: () => set(emptySession),
      clear: () => set(emptySession),
    }),
    {
      name: "agentsql.auth",
      storage: createJSONStorage(() => localStorage),
      partialize: ({ token, expiresAt, username }) => ({ token, expiresAt, username }),
    },
  ),
);

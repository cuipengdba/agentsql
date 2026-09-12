import { useEffect } from "react";
import { Navigate, Outlet, useLocation } from "react-router-dom";

import { me } from "@/api/auth";
import { useAuthStore } from "@/store/authStore";

export function AuthGuard() {
  const token = useAuthStore((state) => state.token);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const setUsername = useAuthStore((state) => state.setUsername);
  const clear = useAuthStore((state) => state.clear);
  const location = useLocation();
  const authenticated = isAuthenticated();

  useEffect(() => {
    if (!authenticated || !token) {
      if (token) {
        clear();
      }
      return;
    }
    let active = true;
    void me()
      .then((profile) => {
        if (active) {
          setUsername(profile.username);
        }
      })
      .catch(() => {
        if (active) {
          clear();
        }
      });
    return () => {
      active = false;
    };
  }, [authenticated, clear, setUsername, token]);

  if (!authenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <Outlet />;
}

import { createBrowserRouter, RouterProvider } from "react-router-dom";

import { AuthGuard } from "@/layouts/AuthGuard";
import { MainLayout } from "@/layouts/MainLayout";
import { Login } from "@/pages/Login";
import { NotFound } from "@/pages/NotFound";
import { menuRoutes } from "@/routes/menu";

const router = createBrowserRouter([
  { path: "/login", element: <Login /> },
  {
    element: <AuthGuard />,
    children: [
      {
        element: <MainLayout />,
        children: [
          ...menuRoutes.map(({ path, element }) => ({ path, element })),
          { path: "*", element: <NotFound /> },
        ],
      },
    ],
  },
]);

export function App() {
  return <RouterProvider router={router} />;
}

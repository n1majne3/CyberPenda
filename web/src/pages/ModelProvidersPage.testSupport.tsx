import { render } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { ModelProvidersPage } from "./ModelProvidersPage";

export function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter>
        <ModelProvidersPage />
      </MemoryRouter>
    </StrictMode>,
  );
}

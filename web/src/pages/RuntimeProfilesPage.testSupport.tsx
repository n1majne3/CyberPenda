import { render } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { RuntimeProfilesPage } from "./RuntimeProfilesPage";

export function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter>
        <RuntimeProfilesPage />
      </MemoryRouter>
    </StrictMode>,
  );
}

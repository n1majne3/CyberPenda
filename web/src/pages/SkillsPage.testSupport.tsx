import { render } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { SkillsPage } from "./SkillsPage";

export function renderPage() {
  return render(
    <StrictMode>
      <MemoryRouter>
        <SkillsPage />
      </MemoryRouter>
    </StrictMode>,
  );
}

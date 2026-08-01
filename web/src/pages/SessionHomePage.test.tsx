import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { SessionHomePage } from "./SessionHomePage";

function renderPage() {
  return render(
    <MemoryRouter>
      <SessionHomePage />
    </MemoryRouter>,
  );
}

describe("SessionHomePage", () => {
  it("labels Non-Project Mode and separates open and archived sessions", async () => {
    mockApi({
      "/api/sessions?lifecycle=archived": {
        sessions: [
          {
            id: "session-archived",
            title: "Archived notes",
            lifecycle: "archived",
            created_at: "2026-08-01T02:00:00Z",
            updated_at: "2026-08-01T02:00:00Z",
            last_activity_at: "2026-08-01T02:00:00Z",
          },
        ],
      },
      "/api/sessions": {
        sessions: [
          {
            id: "session-open",
            title: "Investigate a host",
            lifecycle: "open",
            created_at: "2026-08-01T01:00:00Z",
            updated_at: "2026-08-01T01:00:00Z",
            last_activity_at: "2026-08-01T01:00:00Z",
          },
        ],
      },
    });

    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "Non-Project Sessions" })).toBeInTheDocument();
    expect(screen.getByText(/this is non-project mode/i)).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: /open investigate a host session/i })).toHaveAttribute(
      "href",
      "/sessions/session-open",
    );
    expect(screen.getByRole("link", { name: /open archived notes session/i })).toHaveAttribute(
      "href",
      "/sessions/session-archived",
    );
    expect(screen.getByRole("button", { name: /archive investigate a host/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /restore archived notes/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /delete archived notes/i })).toBeInTheDocument();
  });

  it("creates a session from the accessible initial-input form", async () => {
    const fetchMock = mockApi({
      "/api/sessions?lifecycle=archived": { sessions: [] },
      "/api/sessions": { sessions: [] },
    });
    const user = userEvent.setup();

    renderPage();

    const input = await screen.findByRole("textbox", { name: /initial input/i });
    await user.type(input, "Check the exposed service");
    await user.click(screen.getByRole("button", { name: /create session/i }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/sessions",
        expect.objectContaining({ method: "POST", body: JSON.stringify({ input: "Check the exposed service" }) }),
      );
    });
  });
});

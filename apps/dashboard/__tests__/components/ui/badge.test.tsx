import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Badge } from "@/components/ui/badge";

describe("Badge", () => {
  it("renders children text content", () => {
    render(<Badge>Active</Badge>);
    expect(screen.getByText("Active")).toBeInTheDocument();
  });

  it("renders as a span element by default", () => {
    render(<Badge>Status</Badge>);
    const badge = screen.getByText("Status");
    expect(badge.tagName).toBe("SPAN");
  });

  it("applies the data-slot attribute for styling hooks", () => {
    render(<Badge>Test</Badge>);
    expect(screen.getByText("Test")).toHaveAttribute("data-slot", "badge");
  });

  it("sets data-variant to the chosen variant", () => {
    render(<Badge variant="destructive">Error</Badge>);
    expect(screen.getByText("Error")).toHaveAttribute(
      "data-variant",
      "destructive",
    );
  });

  it("defaults to the 'default' variant when none is specified", () => {
    render(<Badge>Default</Badge>);
    expect(screen.getByText("Default")).toHaveAttribute(
      "data-variant",
      "default",
    );
  });

  it("merges custom className with variant classes", () => {
    render(<Badge className="my-custom-class">Styled</Badge>);
    const badge = screen.getByText("Styled");
    expect(badge.className).toContain("my-custom-class");
    expect(badge.className).toContain("inline-flex");
  });

  it("renders each variant without errors", () => {
    const variants = [
      "default",
      "secondary",
      "destructive",
      "outline",
      "ghost",
      "link",
    ] as const;

    for (const variant of variants) {
      const { unmount } = render(
        <Badge variant={variant}>{variant}-badge</Badge>,
      );
      expect(screen.getByText(`${variant}-badge`)).toHaveAttribute(
        "data-variant",
        variant,
      );
      unmount();
    }
  });

  it("forwards additional HTML attributes", () => {
    render(
      <Badge data-testid="my-badge" role="status">
        Info
      </Badge>,
    );
    const badge = screen.getByTestId("my-badge");
    expect(badge).toHaveAttribute("role", "status");
    expect(badge).toHaveTextContent("Info");
  });
});

import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
  CardFooter,
  CardAction,
} from "@/components/ui/card";

describe("Card", () => {
  it("renders children content", () => {
    render(<Card>Card body</Card>);
    expect(screen.getByText("Card body")).toBeInTheDocument();
  });

  it("applies data-slot='card' for styling hooks", () => {
    render(<Card data-testid="card">Content</Card>);
    expect(screen.getByTestId("card")).toHaveAttribute("data-slot", "card");
  });

  it("merges custom className with default card styles", () => {
    render(
      <Card data-testid="card" className="bg-red-500">
        Styled
      </Card>,
    );
    const card = screen.getByTestId("card");
    expect(card.className).toContain("bg-red-500");
    expect(card.className).toContain("rounded-xl");
  });
});

describe("CardHeader", () => {
  it("renders with data-slot='card-header'", () => {
    render(<CardHeader data-testid="header">Header</CardHeader>);
    const header = screen.getByTestId("header");
    expect(header).toHaveAttribute("data-slot", "card-header");
    expect(header).toHaveTextContent("Header");
  });

  it("merges custom className", () => {
    render(
      <CardHeader data-testid="header" className="p-8">
        H
      </CardHeader>,
    );
    expect(screen.getByTestId("header").className).toContain("p-8");
  });
});

describe("CardTitle", () => {
  it("renders with data-slot='card-title' and font-semibold", () => {
    render(<CardTitle>Dashboard</CardTitle>);
    const title = screen.getByText("Dashboard");
    expect(title).toHaveAttribute("data-slot", "card-title");
    expect(title.className).toContain("font-semibold");
  });
});

describe("CardDescription", () => {
  it("renders with data-slot='card-description'", () => {
    render(<CardDescription>Some description text</CardDescription>);
    const desc = screen.getByText("Some description text");
    expect(desc).toHaveAttribute("data-slot", "card-description");
  });
});

describe("CardContent", () => {
  it("renders children with data-slot='card-content'", () => {
    render(
      <CardContent>
        <p>Inner content</p>
      </CardContent>,
    );
    const content = screen.getByText("Inner content").parentElement!;
    expect(content).toHaveAttribute("data-slot", "card-content");
  });

  it("applies px-6 padding by default", () => {
    render(<CardContent data-testid="content">Body</CardContent>);
    expect(screen.getByTestId("content").className).toContain("px-6");
  });
});

describe("CardFooter", () => {
  it("renders with data-slot='card-footer'", () => {
    render(<CardFooter data-testid="footer">Footer</CardFooter>);
    expect(screen.getByTestId("footer")).toHaveAttribute(
      "data-slot",
      "card-footer",
    );
  });
});

describe("CardAction", () => {
  it("renders with data-slot='card-action'", () => {
    render(<CardAction data-testid="action">Action</CardAction>);
    expect(screen.getByTestId("action")).toHaveAttribute(
      "data-slot",
      "card-action",
    );
  });
});

describe("Card composition", () => {
  it("renders a full card with all sub-components", () => {
    render(
      <Card data-testid="full-card">
        <CardHeader>
          <CardTitle>Performance</CardTitle>
          <CardDescription>Real-time P&L</CardDescription>
          <CardAction>
            <button>Refresh</button>
          </CardAction>
        </CardHeader>
        <CardContent>
          <span>$1,234.56</span>
        </CardContent>
        <CardFooter>
          <span>Updated 5m ago</span>
        </CardFooter>
      </Card>,
    );

    const card = screen.getByTestId("full-card");
    expect(card).toHaveAttribute("data-slot", "card");

    expect(screen.getByText("Performance")).toBeInTheDocument();
    expect(screen.getByText("Real-time P&L")).toBeInTheDocument();
    expect(screen.getByText("$1,234.56")).toBeInTheDocument();
    expect(screen.getByText("Updated 5m ago")).toBeInTheDocument();
    expect(screen.getByText("Refresh")).toBeInTheDocument();
  });

  it("forwards arbitrary HTML props through all sub-components", () => {
    render(
      <Card aria-label="trading card" data-testid="card">
        <CardHeader id="hdr">
          <CardTitle id="title">T</CardTitle>
        </CardHeader>
        <CardContent id="body">B</CardContent>
      </Card>,
    );

    expect(screen.getByTestId("card")).toHaveAttribute(
      "aria-label",
      "trading card",
    );
    expect(document.getElementById("hdr")).toHaveAttribute(
      "data-slot",
      "card-header",
    );
    expect(document.getElementById("title")).toHaveAttribute(
      "data-slot",
      "card-title",
    );
    expect(document.getElementById("body")).toHaveAttribute(
      "data-slot",
      "card-content",
    );
  });
});

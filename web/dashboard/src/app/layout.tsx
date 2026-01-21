import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
  display: "swap",
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
  display: "swap",
});

export const metadata: Metadata = {
  title: {
    default: "CAAM Dashboard",
    template: "%s | CAAM Dashboard",
  },
  description:
    "Coding Agent Account Manager - Control plane for managing AI coding assistant accounts",
  keywords: [
    "AI",
    "coding assistant",
    "account manager",
    "Claude",
    "Codex",
    "Gemini",
  ],
  authors: [{ name: "CAAM Team" }],
  creator: "CAAM",
  openGraph: {
    type: "website",
    locale: "en_US",
    siteName: "CAAM Dashboard",
    title: "CAAM Dashboard",
    description: "Control plane for managing AI coding assistant accounts",
  },
  robots: {
    index: false,
    follow: false,
  },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#f8fafc" },
    { media: "(prefers-color-scheme: dark)", color: "#020617" },
  ],
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body
        className={`${geistSans.variable} ${geistMono.variable} min-h-screen bg-background text-foreground antialiased`}
      >
        {children}
      </body>
    </html>
  );
}

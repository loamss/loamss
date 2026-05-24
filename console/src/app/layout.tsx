import type { Metadata } from "next";
import { Fraunces, IBM_Plex_Sans, IBM_Plex_Mono } from "next/font/google";
import "./globals.css";

/*
 * Type system: three families that need to feel like one family in
 * different registers.
 *
 *   Fraunces      — display serif, variable, optical sizing. Used
 *                   for the wizard step numerals, page titles, and
 *                   any moment that needs editorial weight.
 *
 *   IBM Plex Sans — body text. Technical character, not sterile.
 *
 *   IBM Plex Mono — IDs, paths, hashes, command names. Coherent
 *                   with the sans because they're a paired family.
 *
 * We load only the weights we actually use to keep the static
 * export lean. Variable fonts handle the in-between weights.
 */
const fraunces = Fraunces({
  subsets: ["latin"],
  variable: "--font-fraunces",
  display: "swap",
  // Variable font: pulling the full weight range + opsz/SOFT/WONK
  // axes so the wizard's headlines can use optical sizing for
  // dramatic weight at display scale.
  weight: "variable",
  style: ["normal", "italic"],
  axes: ["opsz", "SOFT", "WONK"],
});

const plexSans = IBM_Plex_Sans({
  subsets: ["latin"],
  variable: "--font-plex-sans",
  display: "swap",
  // IBM Plex Sans is NOT a variable font, so explicit weights.
  weight: ["300", "400", "500", "600"],
  style: ["normal", "italic"],
});

const plexMono = IBM_Plex_Mono({
  subsets: ["latin"],
  variable: "--font-plex-mono",
  display: "swap",
  weight: ["400", "500"],
});

export const metadata: Metadata = {
  title: "Loamss",
  description:
    "Personal data infrastructure. Your data, your storage, your audit log.",
  // No favicon for the prototype; the real one will use the brand
  // mark once the visual identity is finalized.
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html
      lang="en"
      className={`${fraunces.variable} ${plexSans.variable} ${plexMono.variable}`}
    >
      <body className="bg-paper text-ink font-sans antialiased selection:bg-brand/20 selection:text-brand-deep">
        {children}
      </body>
    </html>
  );
}

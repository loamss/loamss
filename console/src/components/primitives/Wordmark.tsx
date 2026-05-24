"use client";

/*
 * Wordmark — the Loamss text mark.
 *
 * Renders the name in Fraunces serif with an italic terminal 's' to
 * give the mark a small piece of personality without commissioning
 * a real logo at the prototype stage. The dot is a small filled
 * square in the brand color — a "you are here" mark that doubles as
 * a status indicator on the dashboard.
 *
 * Sized via the `size` prop. Color via Tailwind text classes on the
 * wrapping element.
 */
interface WordmarkProps {
  size?: "sm" | "md" | "lg";
  showMark?: boolean;
  className?: string;
}

const sizeClasses = {
  sm: "text-base",
  md: "text-xl",
  lg: "text-3xl",
};

export function Wordmark({
  size = "md",
  showMark = true,
  className = "",
}: WordmarkProps) {
  return (
    <span
      className={[
        "inline-flex items-baseline gap-1.5 font-serif leading-none",
        sizeClasses[size],
        className,
      ].join(" ")}
      style={{ fontVariationSettings: "'opsz' 96, 'wght' 500, 'SOFT' 30" }}
    >
      {showMark && (
        <span
          aria-hidden="true"
          className="inline-block bg-brand"
          style={{
            width: size === "sm" ? "5px" : size === "md" ? "7px" : "10px",
            height: size === "sm" ? "5px" : size === "md" ? "7px" : "10px",
            // Slight rotation to break the pure square — feels considered.
            transform: "translateY(-0.05em) rotate(0deg)",
          }}
        />
      )}
      <span>
        Loams<span className="italic">s</span>
      </span>
    </span>
  );
}

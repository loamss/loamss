/**
 * Capsule manifest types — mirrors the schema in capsule-spec.md.
 *
 * The SDK does NOT serialize the manifest to YAML on disk — the
 * capsule author writes capsule.yaml separately and the runtime's
 * `loamss capsule install` validates it. The types here exist so:
 *
 *   1. The SDK can announce serverInfo + protocolVersion during the
 *      MCP initialize handshake from a typed manifest object passed
 *      to `createCapsule({ manifest })`.
 *   2. Capsule authors can build the manifest in TypeScript and dump
 *      it to YAML at build time if they prefer code-as-config.
 *
 * Spec version targeted by this SDK: 0.1.
 */

/** The capsule-spec.md version this SDK targets. */
export const SPEC_VERSION = "0.1" as const;

export interface CapsuleManifest {
	/** Spec version. Defaults to {@link SPEC_VERSION}. */
	spec_version?: string;

	/** Reverse-DNS-ish name (e.g. "com.example.daily-brief"). */
	name: string;

	/** SemVer. */
	version: string;

	/** Short human-readable description. */
	description?: string;

	author: ManifestAuthor;

	/** OS-level requirements the runtime checks before install. */
	requirements?: ManifestRequirements;

	/** Capabilities the capsule declares it needs to function. */
	permissions?: ManifestPermissions;

	/** Tools the capsule exposes; mirrors what tools/list returns. */
	tools?: ManifestTool[];

	/** Resources the capsule exposes; mirrors resources/list. */
	resources?: ManifestResource[];

	/** Memory-extension namespaces the capsule writes under. */
	memory_extensions?: ManifestMemoryExtension[];

	/** Subprocess runtime + entrypoint. */
	runtime?: ManifestRuntime;
}

export interface ManifestAuthor {
	name: string;
	url?: string;
	email?: string;
}

export interface ManifestRequirements {
	min_loamss_version?: string;
	platforms?: string[];
}

export interface ManifestPermissions {
	requires?: ManifestPermissionRequest[];
	uses?: ManifestPermissionRequest[];
}

export interface ManifestPermissionRequest {
	capability: string;
	scope?: Record<string, unknown>;
	default_approval?: boolean;
	rationale?: string;
}

export interface ManifestTool {
	name: string;
	description?: string;
	input_schema?: Record<string, unknown>;
}

export interface ManifestResource {
	uri: string;
	name?: string;
	description?: string;
	mime_type?: string;
}

export interface ManifestMemoryExtension {
	namespace: string;
	description?: string;
}

export interface ManifestRuntime {
	/** Today: "subprocess". Other types reserved. */
	type: "subprocess";
	entrypoint: string;
	args?: string[];
	env?: Record<string, string>;
}

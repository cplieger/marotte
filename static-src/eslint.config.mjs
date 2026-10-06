// Strict typed-linting config.
// References:
//   https://typescript-eslint.io/users/configs/#strict-type-checked
//   https://typescript-eslint.io/users/configs/#stylistic-type-checked
//   https://typescript-eslint.io/getting-started/typed-linting

import js from "@eslint/js";
import tseslint from "typescript-eslint";

export default [
  // 1. Ignore generated/build outputs and configs that don't need linting.
  {
    ignores: [
      // Dependencies (any depth — marotte's web/static-src/node_modules nests deep)
      "**/node_modules/**",
      "**/static/**",
      "**/static-src/dist/**",
      "**/dist/**",
      "**/build/**",
      "**/.next/**",
      "**/.cache/**",
      "**/coverage/**",
      "**/*.min.*",
      "**/*.gen.ts",
      "**/*.gen.js",
      "**/wire/*.gen.ts",
      "**/__mocks__/**",
      "**/__test-helpers__/**",
    ],
  },
  // 2. Strictest official preset combination (typed linting required).
  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  ...tseslint.configs.stylisticTypeChecked,

  // 3. Project setup — projectService auto-discovers tsconfig per file.
  {
    languageOptions: {
      ecmaVersion: 2024,
      sourceType: "module",
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
      globals: {
        window: "readonly",
        document: "readonly",
        navigator: "readonly",
        console: "readonly",
        fetch: "readonly",
        URL: "readonly",
        URLSearchParams: "readonly",
        setTimeout: "readonly",
        clearTimeout: "readonly",
        setInterval: "readonly",
        clearInterval: "readonly",
        requestAnimationFrame: "readonly",
        cancelAnimationFrame: "readonly",
        queueMicrotask: "readonly",
        WebSocket: "readonly",
        EventSource: "readonly",
        AbortController: "readonly",
        Headers: "readonly",
        Request: "readonly",
        Response: "readonly",
        FormData: "readonly",
        Blob: "readonly",
        File: "readonly",
        FileReader: "readonly",
        IntersectionObserver: "readonly",
        MutationObserver: "readonly",
        ResizeObserver: "readonly",
        CustomEvent: "readonly",
        Event: "readonly",
        EventTarget: "readonly",
        TextEncoder: "readonly",
        TextDecoder: "readonly",
        crypto: "readonly",
        btoa: "readonly",
        atob: "readonly",
        self: "readonly",
        ServiceWorkerGlobalScope: "readonly",
        clients: "readonly",
        describe: "readonly",
        it: "readonly",
        test: "readonly",
        expect: "readonly",
        beforeAll: "readonly",
        beforeEach: "readonly",
        afterAll: "readonly",
        afterEach: "readonly",
        vi: "readonly",
      },
    },
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "error",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
          destructuredArrayIgnorePattern: "^_",
          ignoreRestSiblings: true,
        },
      ],
      "@typescript-eslint/consistent-type-imports": [
        "error",
        { prefer: "type-imports", fixStyle: "inline-type-imports" },
      ],
      "@typescript-eslint/no-explicit-any": "error",
      "@typescript-eslint/no-misused-promises": [
        "error",
        { checksVoidReturn: { attributes: false } },
      ],
      "@typescript-eslint/restrict-template-expressions": [
        "error",
        { allowNumber: true, allowBoolean: true, allowNullish: false },
      ],
      "no-console": ["warn", { allow: ["warn", "error", "debug"] }],
      eqeqeq: ["error", "always", { null: "ignore" }],
      curly: ["error", "all"],
      "no-var": "error",
      "prefer-const": "error",
      "no-throw-literal": "error",
      "@typescript-eslint/switch-exhaustiveness-check": [
        "error",
        { considerDefaultExhaustiveForUnions: true },
      ],
    },
  },

  // 4. Tests: typed but with relaxed rules (tests deliberately break invariants).
  {
    files: [
      "**/*.test.ts",
      "**/*.fuzz.test.ts",
      "**/*.property.test.ts",
      "**/test-helpers/**",
      "**/__mocks__/**",
      "**/__test-helpers__/**",
    ],
    rules: {
      "@typescript-eslint/no-non-null-assertion": "off",
      "@typescript-eslint/no-explicit-any": "off",
      "@typescript-eslint/no-unsafe-assignment": "off",
      "@typescript-eslint/no-unsafe-member-access": "off",
      "@typescript-eslint/no-unsafe-call": "off",
      "@typescript-eslint/no-unsafe-argument": "off",
      "@typescript-eslint/no-unsafe-return": "off",
      "@typescript-eslint/unbound-method": "off",
      "@typescript-eslint/require-await": "off",
      "no-console": "off",
    },
  },

  // 5. Generated and config files + tests: drop type-checked rules.
  {
    files: [
      "**/*.gen.ts",
      "**/wire/*.ts",
      "vitest.config.ts",
      "scripts/*.ts",
      "*.config.ts",
      "*.config.mjs",
      "*.config.js",
      "**/*.test.ts",
      "**/*.fuzz.test.ts",
      "**/*.property.test.ts",
      "fc-strict-setup.ts",
    ],
    ...tseslint.configs.disableTypeChecked,
  },

  // 6. Build scripts: a CLI reports its measurement on stdout. Typed by
  // `npm run typecheck:tests` (tsconfig.test.json), not here.
  {
    files: ["scripts/*.ts"],
    rules: { "no-console": "off" },
  },
];

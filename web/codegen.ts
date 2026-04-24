import type { CodegenConfig } from '@graphql-codegen/cli'

const config: CodegenConfig = {
  schema: '../service/graph/schema.graphql',
  documents: 'src/**/*.{ts,tsx}',
  generates: {
    'src/graphql/generated/types.ts': {
      plugins: ['typescript', 'typescript-operations'],
      config: {
        // Backend emits ISO 8601 strings for DateTime scalars; without this
        // they fall back to `any` which propagates through every generated
        // query type. Tightening here gives call sites string-typed
        // timestamps for free.
        scalars: {
          DateTime: 'string',
        },
      },
    },
  },
}

export default config

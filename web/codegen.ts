import type { CodegenConfig } from '@graphql-codegen/cli'

const config: CodegenConfig = {
  schema: '../service/graph/schema.graphql',
  documents: 'src/**/*.{ts,tsx}',
  generates: {
    'src/lib/graphql/types.ts': {
      plugins: ['typescript'],
    },
    'src/lib/graphql/operations.ts': {
      preset: 'import-types',
      presetConfig: {
        typesPath: './types',
      },
      plugins: ['typescript-operations'],
    },
  },
}

export default config

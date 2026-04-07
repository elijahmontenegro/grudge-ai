import type { CodegenConfig } from '@graphql-codegen/cli'

const config: CodegenConfig = {
  schema: '../service/graph/schema.graphql',
  documents: 'src/**/*.{ts,tsx}',
  generates: {
    'src/graphql/generated/types.ts': {
      plugins: ['typescript', 'typescript-operations'],
    },
  },
}

export default config

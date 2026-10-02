import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { initTheme } from './lib/theme'
import { createAppRouter } from './router'
import './index.css'

initTheme()

const root = document.getElementById('root')
if (!root) throw new Error('#root is missing from index.html')

const queryClient = new QueryClient()
const router = createAppRouter(queryClient)

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)

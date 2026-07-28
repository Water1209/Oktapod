import { createContext, useContext } from 'react'
import type { Artifact, Task } from '../../shared/api/client'

export interface FileReviewState {
  open: (artifacts: Artifact[], task: Task) => void
  close: () => void
}

export const FileReviewContext = createContext<FileReviewState | null>(null)

export function useFileReview(): FileReviewState | null {
  return useContext(FileReviewContext)
}

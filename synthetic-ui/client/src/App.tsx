import React, { useState } from 'react';
import ThreadList from './components/ThreadList';
import ChatView from './components/ChatView';
import { api } from './api';
import { AlertCircle } from 'lucide-react';

function App() {
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null);
  const [loadingThread, setLoadingThread] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleArchive = async (id: string) => {
    if (window.confirm('Are you sure you want to archive this thread? This will move the file to the archive directory.')) {
      try {
        await api.archiveThread(id);
        setSelectedThreadId(null);
        alert('Thread archived successfully');
        window.location.reload();
      } catch (err) {
        console.error(err);
        alert('Failed to archive thread');
      }
    }
  };

  return (
    <div className="flex h-screen w-full bg-white dark:bg-gray-950 text-gray-900 dark:text-gray-100 overflow-hidden">
      {/* Sidebar */}
      <div className="w-80 flex-shrink-0 border-r border-gray-200 dark:border-gray-800 h-full">
        <ThreadList
          onSelectThread={setSelectedThreadId}
          selectedThreadId={selectedThreadId}
          isLoading={loadingThread}
        />
      </div>

      {/* Main Content */}
      <main className="flex-1 relative h-full">
        {selectedThreadId ? (
          <ChatView
            threadId={selectedThreadId}
            onArchive={() => handleArchive(selectedThreadId)}
          />
        ) : (
          <div className="flex flex-col items-center justify-center h-full text-gray-400">
            <div className="bg-gray-100 dark:bg-gray-900 p-6 rounded-full mb-4">
              <div className="animate-pulse">
                <AlertCircle size={48} className="opacity-20" />
              </div>
            </div>
            <p className="text-lg font-medium">Select a thread to begin reviewing</p>
            <p className="text-sm">Conversations from synthetic data will appear here.</p>
          </div>
        )}
      </main>

      {/* Global Error Toast (Simplified) */}
      {error && (
        <div className="fixed bottom-4 right-4 bg-red-600 text-white px-4 py-2 rounded-lg shadow-lg flex items-center gap-2 animate-bounce">
          <AlertCircle size={18} />
          {error}
        </div>
      )}
    </div>
  );
}

export default App;

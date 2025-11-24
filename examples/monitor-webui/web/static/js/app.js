let allIssues = [];
let ws = null;
let wsConnected = false;

// WebSocket connection
function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = protocol + '//' + window.location.host + '/ws';

    ws = new WebSocket(wsUrl);

    ws.onopen = function() {
        console.log('WebSocket connected');
        wsConnected = true;
        updateConnectionStatus(true);
    };

    ws.onmessage = function(event) {
        console.log('WebSocket message:', event.data);
        const mutation = JSON.parse(event.data);
        handleMutation(mutation);
    };

    ws.onerror = function(error) {
        console.error('WebSocket error:', error);
        wsConnected = false;
        updateConnectionStatus(false);
    };

    ws.onclose = function() {
        console.log('WebSocket disconnected');
        wsConnected = false;
        updateConnectionStatus(false);
        // Reconnect after 5 seconds
        setTimeout(connectWebSocket, 5000);
    };
}

// Update connection status indicator
function updateConnectionStatus(connected) {
    const statusEl = document.getElementById('connection-status');
    const dotEl = document.getElementById('connection-dot');
    const textEl = document.getElementById('connection-text');

    if (connected) {
        statusEl.className = 'connection-status connected';
        dotEl.className = 'connection-dot connected';
        textEl.textContent = 'Connected';
    } else {
        statusEl.className = 'connection-status disconnected';
        dotEl.className = 'connection-dot disconnected';
        textEl.textContent = 'Disconnected';
    }
}

// Show/hide loading overlay
function setLoading(isLoading) {
    const overlay = document.getElementById('loading-overlay');
    if (isLoading) {
        overlay.classList.add('active');
    } else {
        overlay.classList.remove('active');
    }
}

// Show error message
function showError(message) {
    const errorEl = document.getElementById('error-message');
    errorEl.textContent = message;
    errorEl.classList.add('active');
    setTimeout(() => {
        errorEl.classList.remove('active');
    }, 5000);
}

// Handle mutation event
function handleMutation(mutation) {
    console.log('Mutation:', mutation.type, mutation.issue_id);
    // Refresh data on mutation
    loadStats();
    loadIssues();
}

// Load statistics
async function loadStats() {
    try {
        const response = await fetch('/api/stats');
        if (!response.ok) throw new Error('Failed to load statistics');
        const stats = await response.json();
        document.getElementById('stat-total').textContent = stats.total_issues || 0;
        document.getElementById('stat-in-progress').textContent = stats.in_progress_issues || 0;
        document.getElementById('stat-open').textContent = stats.open_issues || 0;
        document.getElementById('stat-closed').textContent = stats.closed_issues || 0;
    } catch (error) {
        console.error('Error loading statistics:', error);
        showError('Failed to load statistics: ' + error.message);
    }
}

// Load all issues
async function loadIssues() {
    try {
        const response = await fetch('/api/issues');
        if (!response.ok) throw new Error('Failed to load issues');
        allIssues = await response.json();
        filterIssues();
    } catch (error) {
        console.error('Error loading issues:', error);
        showError('Failed to load issues: ' + error.message);
        document.getElementById('issues-tbody').innerHTML = '<tr><td colspan="6" style="text-align: center; color: #721c24;">Error loading issues</td></tr>';
        document.getElementById('issues-card-view').innerHTML = '<div class="empty-state"><div class="empty-state-icon">⚠️</div><p>Error loading issues</p></div>';
    }
}

// Render issues table
function renderIssues(issues) {
    const tbody = document.getElementById('issues-tbody');
    const cardView = document.getElementById('issues-card-view');

    if (!issues || issues.length === 0) {
        const emptyState = '<div class="empty-state"><div class="empty-state-icon">📋</div><h3>No issues found</h3><p>Create your first issue to get started!</p></div>';
        tbody.innerHTML = '<tr><td colspan="6">' + emptyState + '</td></tr>';
        cardView.innerHTML = emptyState;
        return;
    }

    // Render table view
    tbody.innerHTML = issues.map(issue => {
        const statusClass = 'status-' + (issue.status || 'open').toLowerCase().replace('_', '-');
        const priorityClass = 'priority-' + (issue.priority ?? 2);
        return '<tr onclick="showIssueDetail(\'' + issue.id + '\')"><td>' + issue.id + '</td><td>' + issue.title + '</td><td class="' + statusClass + '">' + (issue.status || 'open') + '</td><td class="' + priorityClass + '">P' + (issue.priority ?? 2) + '</td><td>' + (issue.issue_type || 'task') + '</td><td>' + (issue.assignee || '-') + '</td></tr>';
    }).join('');

    // Render card view for mobile
    cardView.innerHTML = issues.map(issue => {
        const statusClass = 'status-' + (issue.status || 'open').toLowerCase().replace('_', '-');
        const priorityClass = 'priority-' + (issue.priority ?? 2);
        let html = '<div class="issue-card" onclick="showIssueDetail(\'' + issue.id + '\')">';
        html += '<div class="issue-card-header">';
        html += '<span class="issue-card-id">' + issue.id + '</span>';
        html += '<span class="' + priorityClass + '">P' + (issue.priority ?? 2) + '</span>';
        html += '</div>';
        html += '<h3 class="issue-card-title">' + issue.title + '</h3>';
        html += '<div class="issue-card-meta">';
        html += '<span class="' + statusClass + '">● ' + (issue.status || 'open') + '</span>';
        html += '<span>Type: ' + (issue.issue_type || 'task') + '</span>';
        if (issue.assignee) html += '<span>👤 ' + issue.assignee + '</span>';
        html += '</div>';
        html += '</div>';
        return html;
    }).join('');
}

// Filter issues
function filterIssues() {
    const statusSelect = document.getElementById('filter-status');
    const selectedStatuses = Array.from(statusSelect.selectedOptions).map(opt => opt.value);
    
    const prioritySelect = document.getElementById('filter-priority');
    const selectedPriorities = Array.from(prioritySelect.selectedOptions).map(opt => parseInt(opt.value));
    
    const searchText = document.getElementById('filter-text').value.toLowerCase();

    const filtered = allIssues.filter(issue => {
        // If statuses are selected, check if issue status is in the selected list
        if (selectedStatuses.length > 0 && !selectedStatuses.includes(issue.status)) return false;
        
        // If priorities are selected, check if issue priority is in the selected list
        if (selectedPriorities.length > 0 && !selectedPriorities.includes(issue.priority)) return false;
        
        if (searchText) {
            const title = (issue.title || '').toLowerCase();
            const description = (issue.description || '').toLowerCase();
            if (!title.includes(searchText) && !description.includes(searchText)) return false;
        }
        return true;
    });

    renderIssues(filtered);
}

// Reload all data
function reloadData() {
    setLoading(true);
    Promise.all([loadStats(), loadIssues()])
        .then(() => {
            setLoading(false);
        })
        .catch(error => {
            console.error('Error reloading data:', error);
            setLoading(false);
            showError('Failed to reload data: ' + error.message);
        });
}

// Show issue detail modal
async function showIssueDetail(issueId) {
    const modal = document.getElementById('issue-modal');
    const modalTitle = document.getElementById('modal-title');
    const modalBody = document.getElementById('modal-body');

    modal.style.display = 'block';
    modalTitle.textContent = 'Loading...';
    modalBody.innerHTML = '<div class="spinner"></div>';

    try {
        const response = await fetch('/api/issues/' + issueId);
        if (!response.ok) throw new Error('Issue not found');
        const issue = await response.json();

        modalTitle.textContent = issue.id + ': ' + issue.title;
        let html = '<p><strong>Status:</strong> ' + issue.status + '</p>';
        html += '<p><strong>Priority:</strong> P' + issue.priority + '</p>';
        html += '<p><strong>Type:</strong> ' + issue.issue_type + '</p>';
        html += '<p><strong>Assignee:</strong> ' + (issue.assignee || 'Unassigned') + '</p>';
        html += '<p><strong>Created:</strong> ' + new Date(issue.created_at).toLocaleString() + '</p>';
        html += '<p><strong>Updated:</strong> ' + new Date(issue.updated_at).toLocaleString() + '</p>';
        if (issue.description) html += '<h3>Description</h3><pre>' + issue.description + '</pre>';
        if (issue.design) html += '<h3>Design</h3><pre>' + issue.design + '</pre>';
        if (issue.notes) html += '<h3>Notes</h3><pre>' + issue.notes + '</pre>';
        if (issue.labels && issue.labels.length > 0) html += '<p><strong>Labels:</strong> ' + issue.labels.join(', ') + '</p>';
        modalBody.innerHTML = html;
    } catch (error) {
        console.error('Error loading issue details:', error);
        showError('Failed to load issue details: ' + error.message);
        modalBody.innerHTML = '<div class="empty-state"><div class="empty-state-icon">⚠️</div><p>Error loading issue details</p></div>';
    }
}

// Close modal
document.querySelector('.close').onclick = function() {
    document.getElementById('issue-modal').style.display = 'none';
};

window.onclick = function(event) {
    const modal = document.getElementById('issue-modal');
    if (event.target == modal) {
        modal.style.display = 'none';
    }
};

// Filter event listeners
document.getElementById('filter-status').addEventListener('change', function() {
    const statusSelect = document.getElementById('filter-status');
    const options = Array.from(statusSelect.options);
    const allSelected = options.every(opt => opt.selected);
    const btn = document.getElementById('toggle-status');
    btn.textContent = allSelected ? 'Select None' : 'Select All';
    filterIssues();
});
document.getElementById('toggle-status').addEventListener('click', function() {
    const statusSelect = document.getElementById('filter-status');
    const options = Array.from(statusSelect.options);
    const allSelected = options.every(opt => opt.selected);
    const btn = document.getElementById('toggle-status');

    if (allSelected) {
        // Select None
        options.forEach(opt => opt.selected = false);
        btn.textContent = 'Select All';
    } else {
        // Select All
        options.forEach(opt => opt.selected = true);
        btn.textContent = 'Select None';
    }
    filterIssues();
});

document.getElementById('filter-priority').addEventListener('change', function() {
    const prioritySelect = document.getElementById('filter-priority');
    const options = Array.from(prioritySelect.options);
    const allSelected = options.every(opt => opt.selected);
    const btn = document.getElementById('toggle-priority');
    btn.textContent = allSelected ? 'Select None' : 'Select All';
    filterIssues();
});

document.getElementById('toggle-priority').addEventListener('click', function() {
    const prioritySelect = document.getElementById('filter-priority');
    const options = Array.from(prioritySelect.options);
    const allSelected = options.every(opt => opt.selected);
    const btn = document.getElementById('toggle-priority');

    if (allSelected) {
        // Select None
        options.forEach(opt => opt.selected = false);
        btn.textContent = 'Select All';
    } else {
        // Select All
        options.forEach(opt => opt.selected = true);
        btn.textContent = 'Select None';
    }
    filterIssues();
});

document.getElementById('filter-text').addEventListener('input', filterIssues);
document.getElementById('clear-text').addEventListener('click', function() {
    document.getElementById('filter-text').value = '';
    filterIssues();
});

// Stat click listeners
function setStatusFilter(statuses) {
    const statusSelect = document.getElementById('filter-status');
    const options = Array.from(statusSelect.options);
    
    options.forEach(opt => {
        if (statuses === 'all') {
            opt.selected = true;
        } else {
            opt.selected = statuses.includes(opt.value);
        }
    });
    
    // Update toggle button text
    const allSelected = options.every(opt => opt.selected);
    const btn = document.getElementById('toggle-status');
    btn.textContent = allSelected ? 'Select None' : 'Select All';
    
    filterIssues();
}

document.getElementById('stat-item-total').addEventListener('click', () => setStatusFilter('all'));
document.getElementById('stat-item-open').addEventListener('click', () => setStatusFilter(['open']));
document.getElementById('stat-item-in-progress').addEventListener('click', () => setStatusFilter(['in_progress']));
document.getElementById('stat-item-closed').addEventListener('click', () => setStatusFilter(['closed']));

// Reload button listener
document.getElementById('reload-button').addEventListener('click', reloadData);

// Initial load
connectWebSocket();
loadStats();
loadIssues();

// Fallback: Refresh every 30 seconds (WebSocket should handle real-time updates)
setInterval(() => {
    if (!wsConnected) {
        loadStats();
        loadIssues();
    }
}, 30000);

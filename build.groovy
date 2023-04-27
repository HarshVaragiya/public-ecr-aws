pipeline {
    
    agent any

    stages {
        stage("login"){
            steps {
                sh "aws ecr get-login-password --region ap-south-2 | docker login --username AWS --password-stdin ${REPOSITORY}"
            }
        }
        stage("build"){
            steps {
                withCredentials([string(credentialsId: 'gotify-url', variable: 'GOTIFY_URL')]) {
                    sh '''
                    curl ${GOTIFY_URL} \
                        -F "title=${JOB_NAME} trigerrered" \
                        -F "message=build #${BUILD_NUMBER} in progress ${BUILD_URL}" \
                        -F "priority=9"
                    '''
                }
                sh "docker build -t ${REPOSITORY}/public-repo-scanner:${IMAGE_TAG} ."
                sh "docker tag ${REPOSITORY}/public-repo-scanner:${IMAGE_TAG} ${REPOSITORY}/public-repo-scanner:${BUILD_NUMBER}"
            }
        }
        stage("push") {
            steps{
                sh "docker push ${REPOSITORY}/public-repo-scanner:${IMAGE_TAG}"
                sh "docker push ${REPOSITORY}/public-repo-scanner:${BUILD_NUMBER}"
            }
        }
        stage("cleanup"){
            steps {
                build('docker-cleanup')
            }
        }
    }
}